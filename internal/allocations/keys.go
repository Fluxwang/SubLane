package allocations

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/storage/db"
)

const maxSchemeKeys = 100

var ErrKeyWindow = errors.New("allocation_key_window_exhausted")

// KeyWindowError reports the exhausted trailing window and when enough usage leaves it.
type KeyWindowError struct {
	WindowSeconds int64
	RetryAfter    int64
}

func (e *KeyWindowError) Error() string { return ErrKeyWindow.Error() }
func (e *KeyWindowError) Unwrap() error { return ErrKeyWindow }

// KeyShare gives one gateway key a percentage of every window budget in a key-share scheme.
type KeyShare struct {
	KeyID  int64            `json:"key_id"`
	Shares []KeyWindowShare `json:"shares"`
}

type KeyWindowShare struct {
	DurationSeconds int64 `json:"duration_seconds"`
	// Share is a percentage of the window budget in hundredths.
	Share int64 `json:"share"`
}

func keyMode(c Config) bool { return c.Mode == "keys" }

func normalizeKeys(c *Config) error {
	if c.Period != "durations" || !validResetSchedule(*c) || len(c.Members) != 0 || c.Total != 0 || c.RatioUnit != "" || len(c.Windows) == 0 || len(c.Windows) > 8 || len(c.Keys) > maxSchemeKeys {
		return ErrInput
	}
	durations := map[int64]bool{}
	for _, window := range c.Windows {
		if window.DurationSeconds < 3600 || window.DurationSeconds > 365*86400 || window.DurationSeconds%3600 != 0 || durations[window.DurationSeconds] || window.Limit <= 0 || window.Limit > 1_000_000_000_000 {
			return ErrInput
		}
		durations[window.DurationSeconds] = true
	}
	keys := map[int64]bool{}
	for index := range c.Keys {
		key := &c.Keys[index]
		if key.KeyID <= 0 || keys[key.KeyID] || len(key.Shares) != len(c.Windows) {
			return ErrInput
		}
		keys[key.KeyID] = true
		covered := map[int64]bool{}
		for _, share := range key.Shares {
			if !durations[share.DurationSeconds] || covered[share.DurationSeconds] || share.Share < 0 || share.Share > 10000 {
				return ErrInput
			}
			covered[share.DurationSeconds] = true
		}
		slices.SortFunc(key.Shares, func(a, b KeyWindowShare) int { return cmp.Compare(a.DurationSeconds, b.DurationSeconds) })
	}
	if c.Keys == nil {
		c.Keys = []KeyShare{}
	}
	c.Members = []Share{}
	slices.SortFunc(c.Windows, func(a, b WindowCondition) int { return cmp.Compare(a.DurationSeconds, b.DurationSeconds) })
	slices.SortFunc(c.Keys, func(a, b KeyShare) int { return cmp.Compare(a.KeyID, b.KeyID) })
	return nil
}

// A window keeps the time it was first added while its duration is unchanged; client values are ignored.
func stampKeyWindows(c *Config, previous []WindowCondition, now int64) {
	added := map[int64]int64{}
	for _, window := range previous {
		added[window.DurationSeconds] = window.AddedAt
	}
	for index := range c.Windows {
		at, ok := added[c.Windows[index].DurationSeconds]
		if !ok {
			at = now
		}
		c.Windows[index].AddedAt = at
	}
}

// Selection is configuration: removed keys lose their binding (and with it their counted usage),
// and newly selected keys start counting from now.
func syncKeyBindings(ctx context.Context, q *db.Queries, scheme, group int64, keys []KeyShare, now int64) error {
	selected := map[int64]bool{}
	for _, key := range keys {
		candidate, err := q.GetAllocationKeyCandidate(ctx, key.KeyID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInput
		}
		if err != nil {
			return err
		}
		if candidate.GroupID != group || candidate.RevokedAt != nil {
			return ErrInput
		}
		selected[key.KeyID] = true
	}
	existing, err := q.ListSchemeKeyBindings(ctx, scheme)
	if err != nil {
		return err
	}
	bound := map[int64]bool{}
	for _, binding := range existing {
		bound[binding.KeyID] = true
		if selected[binding.KeyID] {
			continue
		}
		if err := q.LeaveKeyAllocation(ctx, db.LeaveKeyAllocationParams{KeyID: binding.KeyID, SchemeID: scheme}); err != nil {
			return err
		}
		if err := audit.Record(ctx, q, "allocation.key_leave", "key", audit.ID(binding.KeyID)); err != nil {
			return err
		}
	}
	for _, key := range keys {
		if bound[key.KeyID] {
			continue
		}
		if _, err := q.GetKeyAllocation(ctx, key.KeyID); err == nil {
			return ErrInput
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := q.JoinKeyAllocation(ctx, db.JoinKeyAllocationParams{KeyID: key.KeyID, SchemeID: scheme, JoinedAt: now}); err != nil {
			return err
		}
		if err := audit.Record(ctx, q, "allocation.key_join", "key", audit.ID(key.KeyID)); err != nil {
			return err
		}
	}
	return nil
}

// Key-share edits always apply now; a second edit in the same second replaces that revision.
func saveImmediateRevision(ctx context.Context, q *db.Queries, scheme, now int64, config string) error {
	current, err := q.CurrentAllocationRevision(ctx, db.CurrentAllocationRevisionParams{SchemeID: scheme, EffectiveAt: now})
	if err == nil && current.EffectiveAt == now {
		return q.ReplaceAllocationRevision(ctx, db.ReplaceAllocationRevisionParams{Config: config, SchemeID: scheme, EffectiveAt: now})
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = q.SaveAllocationRevision(ctx, db.SaveAllocationRevisionParams{SchemeID: scheme, EffectiveAt: now, Config: config})
	return err
}

type keyWindowUsage struct {
	seconds  int64
	share    int64
	limit    int64
	used     int64
	tokens   int64
	active   int64
	pending  int64
	risk     riskBudget
	recovery int64
}

func (u keyWindowUsage) exhausted() bool { return u.used >= u.limit }

// Windows trail the current instant: [now-duration+1, now]. Usage counts only after the key joined
// and after the window was added, so neither selection nor a new window charges older history.
func readKeyWindows(ctx context.Context, q *db.Queries, scheme int64, rev Revision, key, joined, now int64) ([]keyWindowUsage, error) {
	index := slices.IndexFunc(rev.Config.Keys, func(k KeyShare) bool { return k.KeyID == key })
	if index < 0 {
		return nil, ErrUnavailable
	}
	shares := rev.Config.Keys[index].Shares
	out := make([]keyWindowUsage, 0, len(rev.Config.Windows))
	for _, window := range rev.Config.Windows {
		share := int64(0)
		for _, value := range shares {
			if value.DurationSeconds == window.DurationSeconds {
				share = value.Share
			}
		}
		from := max(now-window.DurationSeconds+1, joined, window.AddedAt)
		usage, err := q.AllocationKeyUsage(ctx, db.AllocationKeyUsageParams{SchemeID: scheme, KeyID: key, StartedFrom: from, StartedTo: now + 1})
		if err != nil {
			return nil, err
		}
		exposure, err := q.AllocationKeyExposure(ctx, db.AllocationKeyExposureParams{SchemeID: scheme, KeyID: key, StartedFrom: from, StartedTo: now + 1})
		if err != nil {
			return nil, err
		}
		state := keyWindowUsage{
			seconds: window.DurationSeconds, share: share, limit: window.Limit * share / 10000,
			used: usage.Used, tokens: usage.Tokens, active: exposure.Active, pending: exposure.Pending,
		}
		state.risk = currentRiskBudget(state.used, state.limit, state.active)
		if state.exhausted() {
			state.recovery = now + window.DurationSeconds
			charges, err := q.ListAllocationKeyCharges(ctx, db.ListAllocationKeyChargesParams{SchemeID: scheme, KeyID: key, StartedFrom: from, StartedTo: now + 1})
			if err != nil {
				return nil, err
			}
			expired := int64(0)
			for _, charge := range charges {
				expired += charge.Cost
				if state.used-expired < state.limit {
					// An entry started at s stops counting once now-duration+1 > s.
					state.recovery = charge.StartedAt + window.DurationSeconds
					break
				}
			}
		}
		out = append(out, state)
	}
	return out, nil
}

func keySchemeRevision(ctx context.Context, q *db.Queries, scheme, group, now int64) (db.GetAllocationSchemeRow, Revision, bool, error) {
	row, err := q.GetAllocationScheme(ctx, scheme)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.GroupID != group {
		return row, Revision{}, false, ErrUnavailable
	}
	if err != nil {
		return row, Revision{}, false, err
	}
	rev, err := Current(ctx, q, scheme, now)
	if errors.Is(err, ErrUnavailable) {
		return row, rev, false, nil
	}
	return row, rev, err == nil && keyMode(rev.Config), err
}

// checkKey enforces key shares. A disabled key-share scheme lifts its limits instead of refusing,
// unlike member modes; usage is still recorded so re-enabling judges the whole trailing window.
func checkKey(ctx context.Context, q *db.Queries, enabled bool, rev Revision, scheme, key int64, account, model string, now int64) error {
	if !enabled {
		return nil
	}
	if _, err := accountRate(ctx, q, rev, account, model); err != nil {
		return err
	}
	binding, err := q.GetKeyAllocationBinding(ctx, key)
	if errors.Is(err, sql.ErrNoRows) || err == nil && binding.SchemeID != scheme {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	windows, err := readKeyWindows(ctx, q, scheme, rev, key, binding.JoinedAt, now)
	if err != nil {
		return err
	}
	var exhausted *KeyWindowError
	risk := false
	for _, window := range windows {
		if window.exhausted() {
			wait := max(int64(1), window.recovery-now)
			if exhausted == nil || wait > exhausted.RetryAfter {
				exhausted = &KeyWindowError{WindowSeconds: window.seconds, RetryAfter: wait}
			}
		}
		risk = risk || window.risk.limited
	}
	if exhausted != nil {
		return exhausted
	}
	if risk {
		return ErrRisk
	}
	return nil
}

func beginKey(ctx context.Context, q *db.Queries, r Request, enabled bool, rev Revision, now int64) error {
	if err := checkKey(ctx, q, enabled, rev, r.SchemeID, r.KeyID, r.AccountID, r.Model, now); err != nil {
		return err
	}
	if _, err := accountRate(ctx, q, rev, r.AccountID, r.Model); errors.Is(err, ErrUnpriced) {
		// Only a disabled scheme reaches here; an unpriced request has no charge to record.
		return nil
	} else if err != nil {
		return err
	}
	longest := int64(0)
	for _, window := range rev.Config.Windows {
		longest = max(longest, window.DurationSeconds)
	}
	// reset_at drives retention: an entry is prunable once it has left the longest window.
	return q.BeginAllocationEntry(ctx, db.BeginAllocationEntryParams{RequestID: r.ID, SchemeID: r.SchemeID, RevisionID: rev.ID, UserID: r.UserID, KeyID: r.KeyID, AccountID: r.AccountID, Model: r.Model, Mode: "amount", WindowStart: r.StartedAt, ResetAt: r.StartedAt + longest, StartedAt: r.StartedAt})
}

// KeyCandidate is a selectable key of a pool; secrets and hashes never leave storage.
type KeyCandidate struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Owner  string `json:"owner"`
}

func (s *Service) KeyCandidates(ctx context.Context, group int64) ([]KeyCandidate, error) {
	q := db.New(s.conn)
	if _, err := q.GetTenantGroup(ctx, db.GetTenantGroupParams{ID: group, TenantID: s.tenantID}); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInput
	} else if err != nil {
		return nil, err
	}
	rows, err := q.ListAllocationKeyCandidates(ctx, group)
	if err != nil {
		return nil, err
	}
	out := make([]KeyCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, KeyCandidate{ID: row.ID, Name: row.Name, Prefix: row.Prefix, Owner: row.Username})
	}
	return out, nil
}
