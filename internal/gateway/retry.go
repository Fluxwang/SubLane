package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/allocations"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/logging"
	"github.com/murongg/SubLane/internal/storage/db"
	"github.com/murongg/SubLane/internal/upstream"
)

func temporaryStatus(status int) bool {
	switch status {
	case 408, 500, 502, 503, 504:
		return true
	}
	return false
}

func (s *Service) executeWithRetry(ctx context.Context, entry *observation, id, model string, raw []byte, credential accounts.Credential, execute func(accounts.Credential) (*upstream.Stream, error)) (*upstream.Stream, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := execute(credential)
		// Only explicit temporary rejection is replayable. A successful response
		// belongs to its Exchange even if a later stream read fails or times out.
		if err != nil || result == nil || !temporaryStatus(result.StatusCode) || attempt >= s.policy.Retries {
			return result, err
		}
		delay := min(5*time.Second, s.policy.RetryDelay<<attempt)
		if retry := result.Header.Get("Retry-After"); retry != "" {
			wait := time.Duration(retryDelay(retry, s.now(), delay)) * time.Second
			if wait > 5*time.Second {
				return result, nil
			}
			delay = max(delay, wait)
		}
		result.Body.Close()
		logging.From(ctx).LogAttrs(ctx, slog.LevelWarn, "Gateway upstream retry",
			slog.String("request_id", entry.record.RequestID), slog.Int64("tenant_id", s.tenantID),
			slog.String("account_id", id), slog.String("provider", entry.record.Provider),
			slog.Int("upstream_status", result.StatusCode), slog.Int("attempt", attempt+1), slog.String("stage", "scheduled"))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if err := s.recheckAttempt(ctx, entry, id, model, raw); err != nil {
			return nil, err
		}
		credential, err = s.accounts.Prepare(ctx, id, s.provider.Refresh)
		if err != nil {
			return nil, err
		}
	}
}

func (s *Service) recheckAttempt(ctx context.Context, entry *observation, id, model string, raw []byte) error {
	if err := s.checkContent(ctx, entry, raw); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadRuntime(ctx); err != nil {
		return err
	}
	// Recheck grants, key state and model policy from one SQLite snapshot.
	// Commit before credential refresh or provider IO; retries retain their lease.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	if err := s.recheckMember(ctx, q, entry); err != nil {
		return err
	}
	allowed, err := poolAccounts(ctx, q, entry.record.UserID, entry.record.GroupID)
	if err != nil {
		return err
	}
	if !allowed[id] {
		return ErrAffinityUnavailable
	}
	policy, err := groups.ReadPolicy(ctx, q, entry.record.UserID, entry.record.GroupID)
	if err != nil {
		return err
	}
	if !policy.AllowsProvider(entry.record.Provider) || !policy.AllowsResource(entry.record.Provider, model) {
		return ErrModelNotAllowed
	}
	scheme, err := allocations.KeyScheme(ctx, q, entry.record.KeyID, entry.record.UserID, entry.record.GroupID, s.now().Unix())
	if err != nil {
		return err
	}
	if scheme != entry.schemeID {
		return groups.ErrUnavailable
	}
	state := s.health[id]
	if state == nil {
		return accounts.ErrNotFound
	}
	if state.CooldownUntil > s.now().Unix() {
		return &CoolingError{RetryAfter: state.CooldownUntil - s.now().Unix()}
	}
	modelState, err := s.readModelRuntime(ctx, q, id, model)
	if err != nil {
		return err
	}
	if modelState != nil && modelState.cooldownUntil > s.now().Unix() {
		return &CoolingError{RetryAfter: modelState.cooldownUntil - s.now().Unix(), Model: model}
	}
	known, supported, err := s.catalogSupport(ctx, q, id, model, entry.record.Provider)
	if err != nil {
		return err
	}
	if !known {
		return ErrCatalogUnavailable
	}
	if !supported {
		return ErrModelUnavailable
	}
	row, err := q.GetAccount(ctx, db.GetAccountParams{ID: id, TenantID: s.tenantID})
	if err != nil {
		return err
	}
	if !row.Enabled {
		return accounts.ErrDisabled
	}
	if row.Status == "reauth_required" {
		return accounts.ErrReauthorize
	}
	if err := s.quotaAdmission(ctx, q, accounts.Account{ID: id, Provider: row.Provider}, model); err != nil {
		return err
	}
	return tx.Commit()
}
