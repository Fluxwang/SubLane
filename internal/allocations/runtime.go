package allocations

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/storage/db"
)

func unix(n int64) time.Time { return time.Unix(n, 0) }

// A ratio rule stores percentages while entries use the selected accounting unit.
func entryMode(c Config) string {
	if c.Mode == "ratio" {
		return c.RatioUnit
	}
	if c.Mode == "windows" {
		return "amount"
	}
	return c.Mode
}
func effectiveWindow(rev Revision, now int64, location *time.Location) (int64, int64) {
	windows := effectiveWindows(rev, now, location)
	longest := windows[0]
	for _, window := range windows[1:] {
		if window.end > longest.end {
			longest = window
		}
	}
	// Retention follows the latest active window, so short-window cleanup cannot erase longer-window usage.
	return longest.start, longest.end
}
func Authorize(ctx context.Context, q *db.Queries, scheme, user, group, now int64) (Revision, error) {
	row, err := q.GetAllocationScheme(ctx, scheme)
	if err != nil || row.GroupID != group {
		return Revision{}, ErrUnavailable
	}
	allowed, err := q.CanUseAllocation(ctx, db.CanUseAllocationParams{ID: scheme, UserID: user})
	if err != nil {
		return Revision{}, err
	}
	if !allowed {
		return Revision{}, ErrUnavailable
	}
	rev, err := Current(ctx, q, scheme, now)
	if err != nil {
		return rev, err
	}
	for _, m := range rev.Config.Members {
		if m.UserID == user {
			return rev, nil
		}
	}
	return rev, ErrUnavailable
}
func KeyScheme(ctx context.Context, q *db.Queries, key, user, group, now int64) (int64, error) {
	id, err := q.GetKeyAllocation(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		pool, err := q.GetPoolAllocation(ctx, group)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		// Key-share schemes constrain only their selected keys; other pool keys stay unrestricted.
		if _, _, keys, err := keySchemeRevision(ctx, q, pool, group, now); err != nil || keys {
			return 0, err
		}
		return 0, ErrUnavailable
	}
	if err != nil {
		return 0, err
	}
	if _, _, keys, err := keySchemeRevision(ctx, q, id, group, now); err != nil || keys {
		return id, err
	}
	_, err = Authorize(ctx, q, id, user, group, now)
	return id, err
}

// accountRate charges nothing for an unpriced model the account's upstream marks hidden.
// Hidden models are client-internal (for example Codex auto review) and have no public price;
// unpriced visible models are still rejected so a missing price never silently becomes free.
func accountRate(ctx context.Context, q *db.Queries, rev Revision, account, model string) (Rate, error) {
	rate, err := rateFor(rev, model)
	if !errors.Is(err, ErrUnpriced) {
		return rate, err
	}
	row, e := q.GetAccountCatalog(ctx, account)
	if e != nil {
		return rate, err
	}
	catalog, e := accounts.DecodeCatalog(row.ModelsSnapshot, row.ModelsRevision)
	if e != nil || !slices.Contains(catalog.HiddenModels, model) {
		return rate, err
	}
	return Rate{Model: model}, nil
}
func rateFor(rev Revision, model string) (Rate, error) {
	for _, r := range rev.Config.Rates {
		if r.Model == model {
			return r, nil
		}
	}
	return Rate{}, ErrUnpriced
}

const maxOutstandingRiskRequests = 4

type riskBudget struct {
	reserved int64
	room     int64
	limited  bool
}

func currentRiskBudget(used, limit, active int64) riskBudget {
	perRequest := max(int64(1), (limit+9)/10)
	reserved := active * perRequest
	// The buffer is shared headroom; admitting another request still needs one full reservation.
	room := max(int64(0), limit+perRequest-used-reserved)
	return riskBudget{
		reserved: reserved,
		room:     room,
		limited:  active >= maxOutstandingRiskRequests || room < perRequest,
	}
}

type memberWindowUsage struct {
	window    allocationWindow
	limit     int64
	unlimited bool
	used      int64
	tokens    int64
	active    int64
	pending   int64
	risk      riskBudget
}

func readMemberWindow(ctx context.Context, q *db.Queries, scheme, user int64, member Share, config Config, window allocationWindow) (memberWindowUsage, error) {
	mode := entryMode(config)
	usage, err := q.AllocationMemberUsage(ctx, db.AllocationMemberUsageParams{SchemeID: scheme, UserID: user, StartedFrom: window.start, StartedTo: window.end, Mode: mode})
	if err != nil {
		return memberWindowUsage{}, err
	}
	exposure, err := q.AllocationMemberExposure(ctx, db.AllocationMemberExposureParams{SchemeID: scheme, UserID: user, StartedFrom: window.start, StartedTo: window.end, Mode: mode})
	if err != nil {
		return memberWindowUsage{}, err
	}
	limit := member.Limit
	if config.Mode == "ratio" {
		limit = ratioLimit(config.Total, member.Limit)
	} else if config.Mode == "windows" {
		limit = window.limit
		for _, override := range member.WindowOverrides {
			if override.DurationSeconds == window.seconds {
				limit = override.Limit
				break
			}
		}
	}
	unlimited := config.Mode == "windows" && limit == 0
	risk := riskBudget{}
	if !unlimited {
		risk = currentRiskBudget(usage.Used, limit, exposure.Active)
	}
	return memberWindowUsage{
		window: window, limit: limit, unlimited: unlimited, used: usage.Used, tokens: usage.Tokens,
		active: exposure.Active, pending: exposure.Pending,
		risk: risk,
	}, nil
}

func Check(ctx context.Context, q *db.Queries, scheme, user, key, group int64, account, model string, now int64, location *time.Location) (Revision, error) {
	if row, rev, keys, err := keySchemeRevision(ctx, q, scheme, group, now); err != nil || keys {
		if err != nil {
			return rev, err
		}
		return rev, checkKey(ctx, q, row.Enabled == 1, rev, scheme, key, account, model, now)
	}
	rev, err := Authorize(ctx, q, scheme, user, group, now)
	if err != nil {
		return rev, err
	}
	if entryMode(rev.Config) == "amount" {
		if _, err = accountRate(ctx, q, rev, account, model); err != nil {
			return rev, err
		}
	}
	for _, m := range rev.Config.Members {
		if m.UserID != user {
			continue
		}
		riskLimited := false
		for _, window := range effectiveWindows(rev, now, location) {
			state, err := readMemberWindow(ctx, q, scheme, user, m, rev.Config, window)
			if err != nil {
				return rev, err
			}
			if !state.unlimited && state.used >= state.limit {
				return rev, ErrQuota
			}
			riskLimited = riskLimited || state.risk.limited
		}
		if riskLimited {
			return rev, ErrRisk
		}
	}
	return rev, nil
}

type Request struct {
	ID                               string
	SchemeID, UserID, KeyID, GroupID int64
	AccountID, Model                 string
	StartedAt                        int64
}

func Begin(ctx context.Context, q *db.Queries, r Request, now int64, location *time.Location) error {
	if row, rev, keys, err := keySchemeRevision(ctx, q, r.SchemeID, r.GroupID, now); err != nil || keys {
		if err != nil {
			return err
		}
		return beginKey(ctx, q, r, row.Enabled == 1, rev, now)
	}
	rev, err := Check(ctx, q, r.SchemeID, r.UserID, r.KeyID, r.GroupID, r.AccountID, r.Model, now, location)
	if err != nil {
		return err
	}
	start, end := effectiveWindow(rev, now, location)
	if err = q.BeginAllocationEntry(ctx, db.BeginAllocationEntryParams{RequestID: r.ID, SchemeID: r.SchemeID, RevisionID: rev.ID, UserID: r.UserID, AccountID: r.AccountID, Model: r.Model, Mode: entryMode(rev.Config), WindowStart: start, ResetAt: end, StartedAt: r.StartedAt}); err != nil {
		return err
	}
	return nil
}

type Completion struct {
	Input, Output, Cached int64
	Known                 bool
	Dispatched            bool
	Rejected              bool
}

func Finish(ctx context.Context, q *db.Queries, id string, c Completion, now int64, manual bool) error {
	e, err := q.GetAllocationEntry(ctx, id)
	if err != nil {
		return err
	}
	if !manual && e.State != "active" {
		return nil
	}
	if manual {
		if e.State == "settled" && e.Manual == 1 && e.InputTokens == c.Input && e.OutputTokens == c.Output && e.CachedTokens == c.Cached {
			return nil
		}
		if e.State != "pending" || c.Input < e.InputTokens || c.Output < e.OutputTokens {
			return ErrSettlement
		}
	}
	state := "settled"
	if c.Dispatched && !c.Rejected && !c.Known {
		state = "pending"
	}
	cost := c.Input + c.Output
	if c.Input < 0 || c.Output < 0 || c.Cached < 0 || c.Cached > c.Input || c.Input > 1_000_000_000 || c.Output > 1_000_000_000 {
		return ErrInput
	}
	if e.Mode != "tokens" {
		row, err := q.GetAllocationRevision(ctx, e.RevisionID)
		if err != nil {
			return err
		}
		rev, err := decodeRevision(row.ID, row.EffectiveAt, row.Config)
		if err != nil {
			return err
		}
		rate, err := accountRate(ctx, q, rev, e.AccountID, e.Model)
		if err != nil {
			return err
		}
		cost, err = Cost(rate, c.Input, c.Output, c.Cached)
		if err != nil {
			return err
		}
	}
	if state == "pending" {
		// Partial observations remain available for correction but are not a charge.
		cost = 0
	}
	return q.FinishAllocationEntry(ctx, db.FinishAllocationEntryParams{RequestID: id, FinishedAt: now, State: state, InputTokens: c.Input, OutputTokens: c.Output, CachedTokens: c.Cached, Cost: cost, Manual: bit(manual)})
}
func (s *Service) Settle(ctx context.Context, scheme int64, id string, c Completion) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if _, err := q.GetTenantAllocationScheme(ctx, db.GetTenantAllocationSchemeParams{ID: scheme, TenantID: s.tenantID}); err != nil {
		return ErrNotFound
	}
	e, err := q.GetAllocationEntry(ctx, id)
	if err != nil || e.SchemeID != scheme {
		return ErrSettlement
	}
	c.Known = true
	c.Dispatched = true
	if err = Finish(ctx, q, id, c, s.now().UnixMilli(), true); err != nil {
		return err
	}
	if err = audit.Record(ctx, q, "allocation.settle", "allocation", audit.ID(scheme)); err != nil {
		return err
	}
	return tx.Commit()
}

// Prune only closed accounting periods; unresolved requests remain available.
func Prune(ctx context.Context, q *db.Queries, before int64) (int64, error) {
	return q.PruneAllocations(ctx, before)
}
