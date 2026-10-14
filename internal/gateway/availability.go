package gateway

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/capacity"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage/db"
	"github.com/murongg/SubLane/internal/upstream"
)

type availabilityAccount struct {
	account accounts.Account
	catalog accounts.Catalog
	usage   *upstream.Usage
	limits  map[string]*modelRuntime
}

// Availability observes new-request eligibility only. It never reserves a slot,
// refreshes metadata, or creates/moves a conversation affinity.
func (s *Service) Availability(ctx context.Context, userID, groupID int64, model string) (capacity.Model, error) {
	if len(model) == 0 || len(model) > 128 {
		return capacity.Model{}, upstream.ErrInput
	}
	if _, err := accounts.DecodeCatalog([]byte(`{"models":[`+strconv.Quote(model)+`],"updated_at":1}`), 0); err != nil {
		return capacity.Model{}, upstream.ErrInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadRuntime(ctx); err != nil {
		return capacity.Model{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return capacity.Model{}, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	if _, err := q.GetTenantGroup(ctx, db.GetTenantGroupParams{ID: groupID, TenantID: s.tenantID}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = groups.ErrUnavailable
		}
		return capacity.Model{}, err
	}
	policy, err := groups.ReadPolicy(ctx, q, userID, groupID)
	if err != nil {
		return capacity.Model{}, err
	}
	rows, err := s.availabilityAccounts(ctx, q, groupID)
	if err != nil {
		return capacity.Model{}, err
	}
	result := s.modelAvailability(rows, policy, model)
	return result, tx.Commit()
}

func (s *Service) availabilityAccounts(ctx context.Context, q *db.Queries, groupID int64) ([]availabilityAccount, error) {
	ids, err := q.ListGroupAccounts(ctx, groupID)
	if err != nil {
		return nil, err
	}
	result := make([]availabilityAccount, 0, len(ids))
	for _, id := range ids {
		row, err := q.GetAccount(ctx, db.GetAccountParams{ID: id, TenantID: s.tenantID})
		if err != nil {
			return nil, err
		}
		catalog, err := accounts.DecodeCatalog(row.ModelsSnapshot, row.ModelsRevision)
		if err != nil {
			return nil, err
		}
		quota, err := q.GetAccountUsage(ctx, id)
		if err != nil {
			return nil, err
		}
		limits, err := q.ListAvailabilityModelLimits(ctx, db.ListAvailabilityModelLimitsParams{TargetID: id, WorkspaceID: s.tenantID})
		if err != nil {
			return nil, err
		}
		value := availabilityAccount{account: accounts.Account{ID: id, Name: row.Name, Provider: row.Provider, Enabled: row.Enabled, Status: row.Status}, catalog: catalog, usage: savedUsage(quota), limits: map[string]*modelRuntime{}}
		for _, limit := range limits {
			value.limits[limit.Model] = &modelRuntime{cooldownUntil: limit.CooldownUntil, failures: limit.Failures}
		}
		// Failed writes and active recovery leases can be stricter than persisted state.
		for key, state := range s.modelHealth {
			if runtime := s.health[id]; runtime != nil && key.account == id && state.lifecycle == row.ModelsRevision && state.runtimeRevision == runtime.revision {
				value.limits[key.model] = state
			}
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Service) modelAvailability(rows []availabilityAccount, policy groups.ModelPolicy, model string) capacity.Model {
	now := s.now().Unix()
	result := capacity.Model{Model: model, State: "unavailable", ServerTime: now, Reasons: []capacity.Reason{}, Accounts: []capacity.Account{}}
	counts := map[string]int{}
	if !policy.Allows(model) {
		result.Reasons = append(result.Reasons, capacity.Reason{Code: "model_not_allowed", Count: 1})
		return result
	}
	for _, row := range rows {
		account := row.account
		runtime := s.health[account.ID]
		value := capacity.Account{ID: account.ID, Name: account.Name, Provider: account.Provider, Reason: "available", QuotaState: "unknown"}
		if runtime != nil {
			value.InFlight = runtime.InFlight
			value.MaxConcurrency = runtime.MaxConcurrency
		}
		known := catalogUsable(row.catalog, s.catalog.now())
		supported := known && catalogRoutes(row.catalog, model)
		if !supported && row.catalog.Source != s.provider.CatalogSource(account.Provider) {
			known = false
		}
		var reason error
		switch {
		case !s.accounts.ProviderEnabled(account.Provider):
			reason = accounts.ErrProviderDisabled
		case !account.Enabled:
			reason = accounts.ErrDisabled
		case account.Status == "reauth_required":
			reason = accounts.ErrReauthorize
		case !policy.AllowsProvider(account.Provider) || !policy.AllowsResource(account.Provider, model):
			reason = ErrModelNotAllowed
		case !known:
			reason = ErrCatalogUnavailable
		case !supported:
			reason = ErrModelUnavailable
		default:
			reason = s.accountAdmission(account)
			if reason == nil {
				reason = modelRuntimeAdmission(row.limits[upstream.CatalogModelID(model)], now, model)
			}
		}
		quota := quotaDecision{State: "unsupported"}
		if quotaRoutingSupported(account.Provider) {
			quota = quotaDecision{State: "unknown"}
			if row.usage != nil {
				quota = quotaStatus(quotaForModel(*row.usage, account.Provider, model), s.now())
			}
		}
		value.QuotaState = quota.State
		if quota.State == "available" || quota.State == "exhausted" {
			selected := quotaForModel(*row.usage, account.Provider, model)
			for _, limit := range selected.Limits {
				if limit.Name == "" {
					for _, window := range limit.Windows {
						if window.UsedPercent != nil && (value.QuotaUsedPercent == nil || *window.UsedPercent > *value.QuotaUsedPercent) {
							used := *window.UsedPercent
							value.QuotaUsedPercent = &used
						}
					}
				}
			}
		}
		if reason == nil && quota.State == "exhausted" {
			reason = &QuotaError{RetryAfter: max(1, quota.RetryAt-now)}
		}
		if reason != nil {
			value.Reason = reason.Error()
			var cooling *CoolingError
			if errors.As(reason, &cooling) {
				value.Reason = cooling.Unwrap().Error()
				value.RetryAt = now + cooling.RetryAfter
			}
			var exhausted *QuotaError
			if errors.As(reason, &exhausted) {
				value.RetryAt = now + exhausted.RetryAfter
			}
			counts[value.Reason]++
			if errors.Is(reason, ErrCatalogUnavailable) {
				result.Unknown++
			}
			if value.RetryAt > now && (result.RetryAt == 0 || value.RetryAt < result.RetryAt) {
				result.RetryAt = value.RetryAt
			}
		} else {
			result.Available++
		}
		result.Accounts = append(result.Accounts, value)
	}
	if result.Available > 0 {
		result.State = "available"
		result.RetryAt = 0
	} else if result.Unknown > 0 {
		result.State = "unknown"
		result.RetryAt = 0
	}
	if len(rows) == 0 {
		counts[ErrNoAccount.Error()] = 1
	}
	for code, count := range counts {
		result.Reasons = append(result.Reasons, capacity.Reason{Code: code, Count: count})
	}
	sort.Slice(result.Reasons, func(i, j int) bool { return result.Reasons[i].Code < result.Reasons[j].Code })
	return result
}

// WorkspaceCapacity shares the same local observations as administrator diagnostics.
// Only enabled pools and policy-permitted, previously observed models are monitored.
func (s *Service) WorkspaceCapacity(ctx context.Context) ([]capacity.Pool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadRuntime(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	pools, err := q.ListGroups(ctx, s.tenantID)
	if err != nil {
		return nil, err
	}
	result := []capacity.Pool{}
	remaining := 4096
	for _, pool := range pools {
		if !pool.Enabled {
			continue
		}
		rules, err := q.ListGroupModels(ctx, pool.ID)
		if err != nil {
			return nil, err
		}
		policy := groups.ModelPolicy{Restricted: pool.RestrictedModels, Models: rules}
		rows, err := s.availabilityAccounts(ctx, q, pool.ID)
		if err != nil {
			return nil, err
		}
		models := map[string]bool{}
		if policy.Restricted {
			for _, rule := range rules {
				_, native := groups.SplitModel(rule)
				models[native] = true
			}
		} else {
			for _, row := range rows {
				for _, model := range row.catalog.Models {
					models[model] = true
				}
			}
		}
		remaining -= len(models)
		if remaining < 0 {
			return nil, upstream.ErrResponse
		}
		item := capacity.Pool{ID: pool.ID, Models: []capacity.Model{}}
		for _, row := range rows {
			if row.account.Enabled && row.account.Status != "reauth_required" && !catalogUsable(row.catalog, s.catalog.now()) {
				item.Unknown = true
			}
		}
		ids := make([]string, 0, len(models))
		for model := range models {
			ids = append(ids, model)
		}
		sort.Strings(ids)
		for _, model := range ids {
			item.Models = append(item.Models, s.modelAvailability(rows, policy, model))
		}
		result = append(result, item)
	}
	return result, tx.Commit()
}
