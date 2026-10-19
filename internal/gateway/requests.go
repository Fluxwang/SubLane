package gateway

import (
	"context"
	"encoding/json"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/allocations"
	"github.com/murongg/SubLane/internal/content"
	"github.com/murongg/SubLane/internal/pagination"
	"github.com/murongg/SubLane/internal/storage/db"
)

type RequestRecord struct {
	ID                    int64           `json:"id"`
	UserID                int64           `json:"user_id"`
	KeyID                 int64           `json:"key_id"`
	GroupID               int64           `json:"group_id"`
	AccountID             string          `json:"account_id"`
	Provider              string          `json:"provider"`
	Model                 string          `json:"model"`
	Transport             string          `json:"transport"`
	Operation             string          `json:"operation"`
	StartedAt             int64           `json:"started_at"`
	DurationMs            int64           `json:"duration_ms"`
	Outcome               string          `json:"outcome"`
	ErrorCode             string          `json:"error_code"`
	UpstreamStatus        *int64          `json:"upstream_status"`
	InputTokens           *int64          `json:"input_tokens"`
	OutputTokens          *int64          `json:"output_tokens"`
	CachedTokens          *int64          `json:"cached_tokens"`
	RequestID             string          `json:"request_id"`
	FirstTokenMs          *int64          `json:"first_token_ms"`
	ReasoningEffort       string          `json:"reasoning_effort"`
	Username              string          `json:"username"`
	KeyName               string          `json:"key_name"`
	GroupName             string          `json:"group_name"`
	AccountName           string          `json:"account_name"`
	EstimatedCostMicroUSD *int64          `json:"estimated_cost_micro_usd"`
	Content               *content.Result `json:"content,omitempty"`
}
type RequestPage struct {
	pagination.Metadata
	Requests   []RequestRecord `json:"requests"`
	NextCursor int64           `json:"next_cursor"`
}

type RequestFilter struct {
	Cursor                               int64
	Page, Limit                          int64
	AccountID, Outcome, Model, RequestID string
	From, Until, KeyID, MemberID         int64
}

func (s *Service) Requests(ctx context.Context, filter RequestFilter) (RequestPage, error) {
	return s.requests(ctx, 0, filter)
}
func (s *Service) UserRequests(ctx context.Context, userID int64, filter RequestFilter) (RequestPage, error) {
	if userID <= 0 {
		return RequestPage{}, accounts.ErrInput
	}
	// Caller-controlled operator filters can never replace the session's ownership scope.
	filter.MemberID = 0
	filter.AccountID = ""
	return s.requests(ctx, userID, filter)
}
func (s *Service) requests(ctx context.Context, userID int64, f RequestFilter) (RequestPage, error) {
	page := RequestPage{Requests: []RequestRecord{}}
	bounds := pagination.Input{Page: f.Page, Limit: f.Limit, Cursor: f.Cursor}
	if !bounds.Valid() || len(f.AccountID) > 64 || f.KeyID < 0 || f.MemberID < 0 || f.From < 0 || f.Until < 0 || (f.Until != 0 && f.From >= f.Until) || (f.Model != "" && !safeModel.MatchString(f.Model)) || (f.RequestID != "" && !safeRequestID.MatchString(f.RequestID)) {
		return page, accounts.ErrInput
	}
	switch f.Outcome {
	case "", "success", "incomplete", "error", "canceled", "rejected":
	default:
		return page, accounts.ErrInput
	}
	since := s.now().Add(-7 * 24 * time.Hour).Unix()
	// Count and rows must describe one snapshot, including after retention prunes the last page.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	page.Total, err = q.CountRequests(ctx, db.CountRequestsParams{TenantID: s.tenantID, UserID: userID, AccountID: f.AccountID, Outcome: f.Outcome, Since: max(since, f.From), UntilTime: f.Until, MemberID: f.MemberID, KeyID: f.KeyID, Model: f.Model, RequestID: f.RequestID})
	if err != nil {
		return page, err
	}
	page.Metadata = bounds.Resolve(page.Total)
	offset := page.Offset()
	rows, err := q.ListRequests(ctx, db.ListRequestsParams{TenantID: s.tenantID, UserID: userID, Cursor: f.Cursor, AccountID: f.AccountID, Outcome: f.Outcome, Since: max(since, f.From), UntilTime: f.Until, MemberID: f.MemberID, KeyID: f.KeyID, Model: f.Model, RequestID: f.RequestID, PageLimit: page.PageSize + 1, PageOffset: offset})
	if err != nil {
		return page, err
	}
	if err := tx.Commit(); err != nil {
		return page, err
	}
	if int64(len(rows)) > page.PageSize {
		rows = rows[:page.PageSize]
		page.NextCursor = rows[len(rows)-1].ID
	}
	for _, row := range rows {
		record := RequestRecord{
			ID: row.ID, UserID: row.UserID, KeyID: row.KeyID, GroupID: row.GroupID,
			AccountID: row.AccountID, Provider: row.Provider, Model: row.Model,
			Transport: row.Transport, Operation: row.Operation, StartedAt: row.StartedAt,
			DurationMs: row.DurationMs, Outcome: row.Outcome, ErrorCode: row.ErrorCode,
			UpstreamStatus: row.UpstreamStatus, InputTokens: row.InputTokens,
			OutputTokens: row.OutputTokens, CachedTokens: row.CachedTokens,
			RequestID: row.RequestID, FirstTokenMs: row.FirstTokenMs, ReasoningEffort: row.ReasoningEffort,
			Username: row.Username, KeyName: row.KeyName, GroupName: row.GroupName, AccountName: row.AccountName,
			EstimatedCostMicroUSD: s.estimatedCost(row),
		}
		if userID != 0 {
			record.AccountID, record.AccountName = "", ""
		}
		if row.ContentMode != "" || row.ContentCheckFailed != 0 {
			result := content.Result{Mode: row.ContentMode, Revision: row.ContentRevision, RuleIDs: []string{}, CheckFailed: row.ContentCheckFailed != 0}
			if err := json.Unmarshal([]byte(row.ContentRuleIds), &result.RuleIDs); err != nil {
				return page, err
			}
			record.Content = &result
		}
		page.Requests = append(page.Requests, record)
	}
	return page, nil
}

func (s *Service) estimatedCost(row db.ListRequestsRow) *int64 {
	if s.pricing == nil || row.InputTokens == nil || row.OutputTokens == nil {
		return nil
	}
	price, ok := s.pricing.Lookup(row.Model)
	if !ok {
		return nil
	}
	var cached int64
	if row.CachedTokens != nil {
		cached = *row.CachedTokens
	} else if *row.InputTokens != 0 && price.Input != price.Cached {
		// An unknown cache split cannot be treated as full-price input when rates differ.
		return nil
	}
	// This is a current-catalog estimate; allowance debits use their saved revision prices.
	cost, err := allocations.Cost(allocations.Rate{Input: price.Input, Cached: price.Cached, Output: price.Output}, *row.InputTokens, *row.OutputTokens, cached)
	if err != nil {
		return nil
	}
	return &cost
}

type RequestCaller struct {
	UserID   int64  `json:"user_id"`
	KeyID    int64  `json:"key_id"`
	Username string `json:"username"`
	KeyName  string `json:"key_name"`
}

func (s *Service) RequestCallers(ctx context.Context, userID int64) ([]RequestCaller, error) {
	rows, err := s.queries.ListRequestCallers(ctx, db.ListRequestCallersParams{TenantID: s.tenantID, UserID: userID, Since: s.now().Add(-7 * 24 * time.Hour).Unix()})
	if err != nil {
		return nil, err
	}
	result := make([]RequestCaller, 0, len(rows))
	for _, row := range rows {
		result = append(result, RequestCaller(row))
	}
	return result, nil
}
