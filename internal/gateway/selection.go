package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/allocations"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage/db"
	"github.com/murongg/SubLane/internal/upstream"
)

// Called under s.mu by Open, keeping policy, selection and account reservation atomic.
func (s *Service) selectAccount(ctx context.Context, userID, groupID int64, session, provider, model string, kind Kind) (string, [32]byte, error) {
	return s.selectAllocationAccount(ctx, userID, groupID, session, provider, model, kind, 0, pinnedAffinity)
}
func (s *Service) selectAllocationAccount(ctx context.Context, userID, groupID int64, session, provider, model string, kind Kind, scheme int64, affinity affinityMode) (string, [32]byte, error) {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", userID, session)))
	// Preserve upstream session/cache IDs for existing conversations in the former default pool, even after it is renamed.
	if groupID != groups.LegacyID {
		digest = sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s", userID, groupID, session)))
	}
	if userID <= 0 || groupID <= 0 {
		return "", digest, upstream.ErrInput
	}
	if provider != "" && !s.accounts.ProviderEnabled(provider) {
		return "", digest, accounts.ErrProviderDisabled
	}
	if err := s.loadRuntime(ctx); err != nil {
		return "", digest, err
	}
	if session == "" {
		if _, err := rand.Read(digest[:]); err != nil {
			return "", digest, err
		}
	}
	available, err := s.accounts.List(ctx)
	if err != nil {
		return "", digest, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", digest, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	allowed, err := poolAccounts(ctx, q, userID, groupID)
	if err != nil {
		return "", digest, err
	}
	policy, err := groups.ReadPolicy(ctx, q, userID, groupID)
	if err != nil {
		return "", digest, err
	}
	requested := model
	if provider != "" {
		requested = provider + "/" + model
	}
	if model != "" && !policy.Allows(requested) {
		return "", digest, ErrModelNotAllowed
	}
	scope := provider
	// Automatic routing needs one binding across providers so a new catalog or cooldown cannot move a conversation.
	if scope == "" {
		scope = "auto"
	}
	now := s.now().Unix()
	expires := now + 24*3600
	bindingModel := upstream.CatalogModelID(model)
	var replacement *db.ListSessionAffinitiesRow
	if session != "" {
		bindings, err := q.ListSessionAffinities(ctx, db.ListSessionAffinitiesParams{GroupID: groupID, UserID: userID, SessionHash: digest[:], Now: now})
		if err != nil {
			return "", digest, err
		}
		var existing *db.ListSessionAffinitiesRow
		for i := range bindings {
			if bindings[i].Provider == scope {
				existing = &bindings[i]
				break
			}
		}
		if existing == nil && scope == "auto" && len(bindings) > 0 {
			// Old clients could keep separate conversations under one session ID. Guessing would cross identities.
			if len(bindings) != 1 {
				return "", digest, ErrAffinityUnavailable
			}
			existing = &bindings[0]
		}
		if existing != nil {
			id := existing.AccountID
			// After a portable model switch, old opaque state must not be sent
			// to the new account even if it also supports the previous model.
			if affinity == continuationAffinity && existing.Model != "" && bindingModel != existing.Model {
				return id, digest, ErrAffinityUnavailable
			}
			if !allowed[id] {
				return id, digest, ErrAffinityUnavailable
			}
			var bound *accounts.Account
			for i := range available {
				if available[i].ID == id {
					bound = &available[i]
					break
				}
			}
			if bound == nil {
				return id, digest, accounts.ErrNotFound
			}
			if !s.accounts.ProviderEnabled(bound.Provider) {
				return id, digest, accounts.ErrProviderDisabled
			}
			if !policy.AllowsProvider(bound.Provider) {
				return id, digest, ErrAffinityUnavailable
			}
			if kind == Compact && bound.Provider != "codex" {
				return id, digest, upstream.ErrInput
			}
			reselect := false
			if model != "" && bound.Enabled && bound.Status != "reauth_required" {
				known, supported, err := s.catalogSupport(ctx, q, id, model, bound.Provider)
				if err != nil {
					return id, digest, err
				}
				if !known {
					return id, digest, ErrCatalogUnavailable
				}
				if !supported {
					// A missing capability is not a failover signal. Only an explicit
					// model change with portable history may replace a known binding.
					if affinity != portableAffinity || existing.Model == "" || existing.Model == bindingModel {
						return id, digest, ErrAffinityUnavailable
					}
					reselect = true
				}
			}
			if reselect {
				if existing.Provider == scope {
					replacement = existing
				}
			} else {
				if model != "" && !policy.AllowsResource(bound.Provider, model) {
					return id, digest, ErrModelNotAllowed
				}
				if err := s.accountAdmission(*bound); err != nil {
					return id, digest, err
				}
				if err := s.modelAdmission(ctx, q, id, model); err != nil {
					return id, digest, err
				}
				if err := s.quotaAdmission(ctx, q, *bound, model); err != nil {
					return id, digest, err
				}
				if scheme != 0 {
					if _, err := allocations.Check(ctx, q, scheme, userID, groupID, id, model, now, s.location()); err != nil {
						return id, digest, err
					}
				}
				if bindingModel == "" {
					bindingModel = existing.Model
				}
				if existing.Provider != scope {
					if err := rememberAffinity(ctx, q, userID, groupID, digest, scope, id, bindingModel, now, expires); err != nil {
						return "", digest, err
					}
				}
				if err := q.TouchAccountAffinity(ctx, db.TouchAccountAffinityParams{GroupID: groupID, Provider: existing.Provider, UserID: userID, SessionHash: digest[:], Model: bindingModel, ExpiresAt: expires, Threshold: expires - 1800}); err != nil {
					return "", digest, err
				}
				if err := tx.Commit(); err != nil {
					return "", digest, err
				}
				return id, digest, nil
			}
		}
	}
	candidates := make([]string, 0, len(available))
	preferred := preferredProvider(kind)
	bestRank := 4
	busy, unknown, eligible := false, false, false
	var cooling, quotaWait int64
	var coolingModel string
	var allocationErr error
	for _, account := range available {
		if !s.accounts.ProviderEnabled(account.Provider) || !policy.AllowsProvider(account.Provider) || !allowed[account.ID] || !account.Enabled || account.Status == "reauth_required" || (provider != "" && account.Provider != provider) || (kind == Compact && account.Provider != "codex") {
			continue
		}
		if model != "" && !policy.AllowsResource(account.Provider, model) {
			continue
		}
		eligible = true
		if model != "" {
			known, supported, err := s.catalogSupport(ctx, q, account.ID, model, account.Provider)
			if err != nil {
				return "", digest, err
			}
			if !known {
				unknown = true
				continue
			}
			if !supported {
				continue
			}
		}
		if err := s.accountAdmission(account); err != nil {
			if errors.Is(err, ErrAccountBusy) {
				busy = true
			}
			var wait *CoolingError
			if errors.As(err, &wait) && (cooling == 0 || wait.RetryAfter < cooling) {
				cooling = wait.RetryAfter
				coolingModel = ""
			}
			continue
		}
		if err := s.quotaAdmission(ctx, q, account, model); err != nil {
			var quota *QuotaError
			if !errors.As(err, &quota) {
				return "", digest, err
			}
			if quotaWait == 0 || quota.RetryAfter < quotaWait {
				quotaWait = quota.RetryAfter
			}
			continue
		}
		if err := s.modelAdmission(ctx, q, account.ID, model); err != nil {
			if errors.Is(err, ErrAccountBusy) {
				busy = true
				continue
			}
			var wait *CoolingError
			if errors.As(err, &wait) {
				if cooling == 0 || wait.RetryAfter < cooling {
					cooling = wait.RetryAfter
					coolingModel = wait.Model
				}
				continue
			}
			return "", digest, err
		}
		if scheme != 0 {
			if _, err := allocations.Check(ctx, q, scheme, userID, groupID, account.ID, model, now, s.location()); err != nil {
				if !errors.Is(err, allocations.ErrQuota) && !errors.Is(err, allocations.ErrPending) && !errors.Is(err, allocations.ErrRisk) && !errors.Is(err, allocations.ErrUnpriced) && !errors.Is(err, allocations.ErrUnavailable) {
					return "", digest, err
				}
				allocationErr = err
				continue
			}
		}
		// Preference only ranks accounts that passed every policy and availability check.
		// Existing affinities returned above must never move to a more direct provider.
		rank := policy.Routing.Rank(account.Provider, preferred)
		if rank < bestRank {
			candidates = candidates[:0]
			bestRank = rank
		}
		if rank == bestRank {
			candidates = append(candidates, account.ID)
		}
	}
	if len(candidates) == 0 {
		if allocationErr != nil {
			return "", digest, allocationErr
		}
		if busy {
			return "", digest, ErrAccountBusy
		}
		if cooling > 0 {
			return "", digest, &CoolingError{RetryAfter: cooling, Model: coolingModel}
		}
		if quotaWait > 0 {
			return "", digest, &QuotaError{RetryAfter: quotaWait}
		}
		if unknown {
			return "", digest, ErrCatalogUnavailable
		}
		if eligible && model != "" {
			return "", digest, ErrModelUnavailable
		}
		return "", digest, ErrNoAccount
	}
	// Isolate rotation across preference tiers so traffic using another protocol cannot starve this pool.
	cursor := fmt.Sprintf("%d:%s:%s:%s:%d", groupID, scope, policy.Routing.Preference, preferred, bestRank)
	id := candidates[s.next[cursor]%len(candidates)]
	s.next[cursor] = (s.next[cursor] + 1) % len(candidates)
	if session != "" {
		if replacement != nil {
			err = q.ReplaceAccountAffinity(ctx, db.ReplaceAccountAffinityParams{UserID: userID, GroupID: groupID, SessionHash: digest[:], Provider: scope, AccountID: id, Model: bindingModel, ExpiresAt: expires})
		} else {
			err = rememberAffinity(ctx, q, userID, groupID, digest, scope, id, bindingModel, now, expires)
		}
		if err != nil {
			return "", digest, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", digest, err
	}
	return id, digest, nil
}

func preferredProvider(kind Kind) string {
	switch kind {
	case Messages:
		return "claude"
	case Gemini, GeminiStream:
		return "antigravity"
	default:
		return "codex"
	}
}

func rememberAffinity(ctx context.Context, q *db.Queries, userID, groupID int64, digest [32]byte, scope, id, model string, now, expires int64) error {
	if err := q.PruneAccountAffinity(ctx, now); err != nil {
		return err
	}
	count, err := q.CountAccountAffinity(ctx)
	if err != nil {
		return err
	}
	if count >= 4096 {
		return ErrAffinityLimit
	}
	return q.CreateAccountAffinity(ctx, db.CreateAccountAffinityParams{GroupID: groupID, Provider: scope, UserID: userID, SessionHash: digest[:], AccountID: id, Model: model, ExpiresAt: expires})
}
