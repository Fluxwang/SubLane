package gateway

import (
	"context"
	"database/sql"
	"errors"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage/db"
)

var (
	ErrMemberBusy = errors.New("member_busy")
	ErrMemberRate = errors.New("member_rate_limited")
)

type MemberRateError struct{ RetryAfter int64 }

func (e *MemberRateError) Error() string { return ErrMemberRate.Error() }
func (e *MemberRateError) Unwrap() error { return ErrMemberRate }

type MemberLimit struct {
	UserID             int64 `json:"user_id"`
	RequestsPerMinute  int64 `json:"requests_per_minute"`
	MaxConcurrency     int64 `json:"max_concurrency"`
	InFlight           int64 `json:"in_flight"`
	RequestsThisMinute int64 `json:"requests_this_minute"`
	ResetAt            int64 `json:"reset_at"`
}

func (s *Service) MemberLimits(ctx context.Context, userID int64) (MemberLimit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, err := s.queries.GetMemberLimits(ctx, db.GetMemberLimitsParams{TenantID: s.tenantID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return MemberLimit{}, auth.ErrMemberNotFound
	}
	if err != nil {
		return MemberLimit{}, err
	}
	now := s.now().Unix()
	value := MemberLimit{UserID: userID, RequestsPerMinute: policy.RequestsPerMinute, MaxConcurrency: policy.MaxConcurrency, InFlight: s.memberActive[userID], ResetAt: now - now%60 + 60}
	window, err := s.queries.GetMemberWindow(ctx, db.GetMemberWindowParams{TenantID: s.tenantID, UserID: userID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return MemberLimit{}, err
	}
	if err == nil && window.WindowStart == now-now%60 {
		value.RequestsThisMinute = window.Requests
	}
	return value, nil
}

func (s *Service) SetMemberLimits(ctx context.Context, userID, rpm, concurrency int64) error {
	if userID <= 0 || rpm < 0 || rpm > 6000 || concurrency < 0 || concurrency > 1000 {
		return accounts.ErrInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	n, err := q.SetMemberLimits(ctx, db.SetMemberLimitsParams{TenantID: s.tenantID, UserID: userID, RequestsPerMinute: rpm, MaxConcurrency: concurrency})
	if err != nil {
		return err
	}
	if n != 1 {
		return auth.ErrMemberNotFound
	}
	if err := audit.Record(ctx, q, "member.limits", "member", audit.ID(userID)); err != nil {
		return err
	}
	return tx.Commit()
}

// Called with s.mu held. All keys, transports and groups consume the same member lease.
func (s *Service) admitMember(ctx context.Context, userID int64) error {
	policy, err := s.queries.GetMemberLimits(ctx, db.GetMemberLimitsParams{TenantID: s.tenantID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) || err == nil && !policy.Enabled {
		return groups.ErrUnavailable
	}
	if err != nil {
		return err
	}
	if policy.MaxConcurrency > 0 && s.memberActive[userID] >= policy.MaxConcurrency {
		return ErrMemberBusy
	}
	if policy.RequestsPerMinute > 0 {
		now := s.now().Unix()
		start := now - now%60
		n, err := s.queries.TakeMemberRate(ctx, db.TakeMemberRateParams{TenantID: s.tenantID, UserID: userID, WindowStart: start, RateLimit: policy.RequestsPerMinute})
		if err != nil {
			return err
		}
		if n == 0 {
			return &MemberRateError{RetryAfter: start + 60 - now}
		}
	}
	s.memberActive[userID]++
	return nil
}
