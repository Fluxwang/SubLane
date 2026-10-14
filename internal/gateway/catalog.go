package gateway

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage/db"
	"github.com/murongg/SubLane/internal/upstream"
)

const catalogTTL = 15 * time.Minute
const catalogMaxAge = 24 * time.Hour
const catalogCooldown = 5 * time.Second

var ErrCatalogUnavailable = errors.New("model_catalog_unavailable")
var ErrModelUnavailable = errors.New("model_not_available")

type CatalogSnapshot struct {
	accounts.Catalog
	Known             bool  `json:"known"`
	Stale             bool  `json:"stale"`
	Usable            bool  `json:"usable"`
	Refreshing        bool  `json:"refreshing"`
	RefreshFailed     bool  `json:"refresh_failed"`
	ServerTime        int64 `json:"server_time"`
	RetryAfterSeconds int64 `json:"retry_after_seconds"`
	err               error
	sourceChanged     bool
}

type catalogEntry struct {
	revision   int64
	source     string
	provider   string
	observedAt int64
	flight     chan struct{}
	retryAt    time.Time
	accessed   time.Time
	err        error
}

type catalogCache struct {
	mu      sync.Mutex
	entries map[string]*catalogEntry
	slots   chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	closed  bool
	now     func() time.Time
}

func newCatalogCache(parent context.Context) *catalogCache {
	ctx, cancel := context.WithCancel(parent)
	return &catalogCache{entries: map[string]*catalogEntry{}, slots: make(chan struct{}, 2), ctx: ctx, cancel: cancel, now: time.Now}
}
func (c *catalogCache) close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.workers.Wait()
}

func catalogUsable(value accounts.Catalog, now time.Time) bool {
	return value.UpdatedAt > 0 && now.Unix() >= value.UpdatedAt && now.Unix()-value.UpdatedAt < int64(catalogMaxAge.Seconds())
}
func (c *catalogCache) state(value accounts.Catalog, e *catalogEntry, source string) CatalogSnapshot {
	now := c.now()
	result := CatalogSnapshot{Catalog: value, Known: value.UpdatedAt > 0, ServerTime: now.Unix(), Usable: catalogUsable(value, now)}
	result.sourceChanged = value.Source != source
	result.Stale = result.sourceChanged || !result.Known || !result.Usable || now.Unix()-value.UpdatedAt >= int64(catalogTTL.Seconds())
	if e != nil && e.revision == value.Revision {
		// A group SQL snapshot may precede a just-committed observation; keep readers polling once.
		result.Refreshing = e.flight != nil || errors.Is(e.err, ErrBusy) || value.UpdatedAt < e.observedAt
		result.RefreshFailed = e.err != nil && !errors.Is(e.err, ErrBusy)
		result.err = e.err
		result.RetryAfterSeconds = max(0, int64(math.Ceil(e.retryAt.Sub(now).Seconds())))
		result.Stale = result.Stale || result.RefreshFailed
	}
	return result
}

// AccountCatalog reads persisted capabilities. Only explicit refresh waits; ordinary reads
// return their snapshot immediately while a bounded shared refresh runs in the background.
func (s *Service) AccountCatalog(ctx context.Context, id string, force bool) (CatalogSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CatalogSnapshot{}, err
	}
	account, err := s.accounts.Get(ctx, id)
	if err != nil {
		return CatalogSnapshot{}, err
	}
	if !s.accounts.ProviderEnabled(account.Provider) {
		return CatalogSnapshot{}, accounts.ErrProviderDisabled
	}
	if !account.Enabled {
		return CatalogSnapshot{}, accounts.ErrDisabled
	}
	if account.Status == "reauth_required" {
		return CatalogSnapshot{}, accounts.ErrReauthorize
	}
	c := s.catalog
	c.mu.Lock()
	if c.closed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return CatalogSnapshot{}, context.Canceled
	}
	// Read while refresh completion is locked so an older SQL result cannot look idle and unknown.
	saved, err := s.accounts.Catalog(ctx, id)
	if err != nil {
		c.mu.Unlock()
		return CatalogSnapshot{}, err
	}
	now := c.now()
	e := c.entries[id]
	if e == nil || e.revision != saved.Revision || e.source != s.provider.CatalogSource(account.Provider) {
		if len(c.entries) >= 100 && e == nil {
			oldest := ""
			var at time.Time
			for key, entry := range c.entries {
				if entry.flight == nil && (oldest == "" || entry.accessed.Before(at)) {
					oldest = key
					at = entry.accessed
				}
			}
			if oldest == "" {
				c.mu.Unlock()
				return CatalogSnapshot{}, ErrBusy
			}
			delete(c.entries, oldest)
		}
		e = &catalogEntry{revision: saved.Revision, provider: account.Provider, source: s.provider.CatalogSource(account.Provider), retryAt: time.Unix(saved.UpdatedAt, 0).Add(catalogCooldown)}
		// A cooldown from another discovery contract must not postpone an upgrade refresh.
		if saved.Source != e.source {
			e.retryAt = time.Time{}
		}
		c.entries[id] = e
	}
	e.accessed = now
	state := c.state(saved, e, s.provider.CatalogSource(account.Provider))
	if e.flight == nil && (force || state.Stale) && !now.Before(e.retryAt) {
		select {
		case c.slots <- struct{}{}:
			release, admissionErr := s.Acquire()
			if admissionErr != nil {
				<-c.slots
				e.err = admissionErr
				e.retryAt = now
			} else {
				e.flight = make(chan struct{})
				e.retryAt = now.Add(catalogCooldown)
				c.workers.Add(1)
				go s.fetchCatalog(id, e, release)
			}
		default:
			e.err = ErrBusy
			e.retryAt = now
		}
	}
	flight := e.flight
	state = c.state(saved, e, s.provider.CatalogSource(account.Provider))
	c.mu.Unlock()
	if !force || flight == nil {
		return state, nil
	}
	select {
	case <-ctx.Done():
		return CatalogSnapshot{}, ctx.Err()
	case <-flight:
	}
	// Lifecycle and revision checks also apply after waiting on another request's work.
	account, err = s.accounts.Get(ctx, id)
	if err != nil {
		return CatalogSnapshot{}, err
	}
	if !account.Enabled {
		return CatalogSnapshot{}, accounts.ErrDisabled
	}
	if account.Status == "reauth_required" {
		return CatalogSnapshot{}, accounts.ErrReauthorize
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	saved, err = s.accounts.Catalog(ctx, id)
	if err != nil {
		return CatalogSnapshot{}, err
	}
	return c.state(saved, c.entries[id], s.provider.CatalogSource(account.Provider)), nil
}

func (s *Service) fetchCatalog(id string, e *catalogEntry, release func()) {
	c := s.catalog
	defer c.workers.Done()

	ctx, cancel := context.WithTimeout(c.ctx, 45*time.Second)
	defer cancel()
	discovery, err := readAccount(ctx, s, id, s.provider.Discover)
	observedAt := c.now().Unix()
	if err == nil && discovery.Source != s.provider.CatalogSource(e.provider) {
		err = accounts.ErrCatalogChanged
	}
	if err == nil {
		ids := make([]string, 0, len(discovery.Models))
		for _, model := range discovery.Models {
			ids = append(ids, model.ID)
		}
		err = s.accounts.SaveCatalog(ctx, id, e.revision, ids, discovery.Hidden, observedAt, discovery.Source)
	}
	release()
	<-c.slots
	c.mu.Lock()
	defer c.mu.Unlock()
	e.err = err
	if err == nil {
		e.observedAt = observedAt
	}
	e.retryAt = c.now().Add(catalogCooldown)
	if err != nil {
		e.retryAt = c.now().Add(30 * time.Second)
	}
	close(e.flight)
	e.flight = nil
}

func (s *Service) waitCatalog(ctx context.Context, id string) (CatalogSnapshot, error) {
	value, err := s.AccountCatalog(ctx, id, false)
	if err != nil || (value.Known && !value.sourceChanged) || !value.Refreshing {
		return value, err
	}
	// Joining a cold discovery does not force another refresh of already known data.
	c := s.catalog
	c.mu.Lock()
	e := c.entries[id]
	var flight chan struct{}
	if e != nil {
		flight = e.flight
	}
	c.mu.Unlock()
	if flight != nil {
		select {
		case <-ctx.Done():
			return CatalogSnapshot{}, ctx.Err()
		case <-flight:
		}
	}
	return s.AccountCatalog(ctx, id, false)
}

type GroupCatalog struct {
	Models          []upstream.Model `json:"models"`
	KnownAccounts   int              `json:"known_accounts"`
	UnknownAccounts int              `json:"unknown_accounts"`
	StaleAccounts   int              `json:"stale_accounts"`
	Refreshing      bool             `json:"refreshing"`
	RefreshFailed   bool             `json:"refresh_failed"`
	ServerTime      int64            `json:"server_time"`
}

func (s *Service) warmCatalogs(ctx context.Context, userID, groupID int64, provider string, wait bool) error {
	if wait {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	allowed, err := poolAccounts(ctx, s.queries, userID, groupID)
	if err != nil {
		return err
	}
	policy, err := s.modelPolicy(ctx, userID, groupID)
	if err != nil {
		return err
	}
	if policy.Restricted && len(policy.Models) == 0 {
		return nil
	}
	rows, err := s.accounts.List(ctx)
	if err != nil {
		return err
	}
	ids := []string{}
	for _, account := range rows {
		if !s.accounts.ProviderEnabled(account.Provider) || !allowed[account.ID] || !account.Enabled || account.Status == "reauth_required" || !policyHasProvider(policy, account.Provider) || (provider != "" && account.Provider != provider) {
			continue
		}
		ids = append(ids, account.ID)
		if _, err := s.AccountCatalog(ctx, account.ID, false); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if wait {
		for _, id := range ids {
			if _, err := s.waitCatalog(ctx, id); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return nil
}

func (s *Service) GroupCatalog(ctx context.Context, userID, groupID int64, wait bool) (GroupCatalog, error) {
	result := GroupCatalog{Models: []upstream.Model{}, ServerTime: s.catalog.now().Unix()}
	if err := s.warmCatalogs(ctx, userID, groupID, "", wait); err != nil {
		return result, err
	}
	// Membership, policy, account status and saved capabilities share one final SQLite snapshot.
	// No upstream IO runs in this transaction, including when warming raced with a group edit.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	policy, err := groups.ReadPolicy(ctx, q, userID, groupID)
	if err != nil {
		return result, err
	}
	rows, err := q.ListGroupCatalogs(ctx, groupID)
	if err != nil {
		return result, err
	}
	// Release SQLite before consulting refresh state; account readers take these locks in the opposite order.
	if err := tx.Commit(); err != nil {
		return result, err
	}
	seen := map[string]int{}
	for _, account := range rows {
		if !s.accounts.ProviderEnabled(account.Provider) || !policyHasProvider(policy, account.Provider) {
			continue
		}
		saved, err := accounts.DecodeCatalog(account.ModelsSnapshot, account.ModelsRevision)
		if err != nil {
			return result, err
		}
		c := s.catalog
		c.mu.Lock()
		snapshot := c.state(saved, c.entries[account.ID], s.provider.CatalogSource(account.Provider))
		c.mu.Unlock()
		result.Refreshing = result.Refreshing || snapshot.Refreshing
		result.RefreshFailed = result.RefreshFailed || snapshot.RefreshFailed
		if snapshot.Stale {
			result.StaleAccounts++
		}
		if !snapshot.Usable {
			result.UnknownAccounts++
			continue
		}
		result.KnownAccounts++
		ids := snapshot.Models
		if policy.Restricted {
			ids = []string{}
			for _, permitted := range policy.Models {
				provider, native := groups.SplitModel(permitted)
				if (provider == "" || provider == account.Provider) && catalogContains(snapshot.Models, native) {
					ids = append(ids, native)
				}
			}
		}
		for _, id := range ids {
			if !policy.AllowsResource(account.Provider, id) {
				continue
			}
			metadata := modelMetadata(account.Provider, id)
			if index, ok := seen[id]; ok {
				// A shared native ID has multiple routes, not an arbitrary single owner.
				if result.Models[index].OwnedBy != metadata.OwnedBy {
					result.Models[index].OwnedBy = "sublane"
				}
				continue
			}
			if len(seen) >= 4096 {
				return result, upstream.ErrResponse
			}
			seen[id] = len(result.Models)
			result.Models = append(result.Models, metadata)
		}
	}
	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].ID < result.Models[j].ID })
	return result, nil
}

func (s *Service) catalogSupport(ctx context.Context, q *db.Queries, id, model, provider string) (bool, bool, error) {
	row, err := q.GetAccountCatalog(ctx, id)
	if err != nil {
		return false, false, err
	}
	value, err := accounts.DecodeCatalog(row.ModelsSnapshot, row.ModelsRevision)
	if err != nil {
		return false, false, err
	}
	known := catalogUsable(value, s.catalog.now())
	supported := known && catalogRoutes(value, model)
	// An obsolete catalog can retain known capabilities, but cannot prove a new model unsupported.
	if !supported && value.Source != s.provider.CatalogSource(provider) {
		return false, false, nil
	}
	return known, supported, nil
}

// Routing accepts hidden models; public listings use catalogContains on visible models only.
func catalogRoutes(value accounts.Catalog, model string) bool {
	return value.Contains(model) || value.Contains(upstream.CatalogModelID(model))
}

func catalogContains(models []string, model string) bool {
	return slices.Contains(models, model) || slices.Contains(models, upstream.CatalogModelID(model))
}

func modelMetadata(provider, id string) upstream.Model {
	owner := provider
	if provider == "codex" {
		owner = "openai"
	}
	return upstream.Model{ID: id, Object: "model", OwnedBy: owner}
}

func (s *Service) Models(ctx context.Context, userID, groupID int64) ([]upstream.Model, error) {
	value, err := s.GroupCatalog(ctx, userID, groupID, true)
	if err != nil {
		return nil, err
	}
	if len(value.Models) == 0 && value.UnknownAccounts > 0 {
		return nil, ErrCatalogUnavailable
	}
	return value.Models, nil
}
func (s *Service) Check(ctx context.Context, id string) ([]upstream.Model, error) {
	snapshot, err := s.AccountCatalog(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if snapshot.RefreshFailed {
		return nil, snapshot.err
	}
	if !snapshot.Known {
		return nil, ErrCatalogUnavailable
	}
	account, err := s.accounts.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	result := make([]upstream.Model, 0, len(snapshot.Models))
	for _, id := range snapshot.Models {
		result = append(result, modelMetadata(account.Provider, id))
	}
	return result, nil
}
