package gateway

import (
	"context"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/pricing"
	"github.com/murongg/SubLane/internal/storage/db"
)

type Metrics struct {
	Requests       int64 `json:"requests"`
	Completed      int64 `json:"completed"`
	Incomplete     int64 `json:"incomplete"`
	Errors         int64 `json:"errors"`
	Canceled       int64 `json:"canceled"`
	Rejected       int64 `json:"rejected"`
	DurationMs     int64 `json:"duration_ms"`
	InputTokens    int64 `json:"input_tokens"`
	OutputTokens   int64 `json:"output_tokens"`
	CachedTokens   int64 `json:"cached_tokens"`
	InputReported  int64 `json:"input_reported"`
	OutputReported int64 `json:"output_reported"`
	CachedReported int64 `json:"cached_reported"`
}
type Statistic struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Metrics
}
type Statistics struct {
	FromDay       int64       `json:"from_day"`
	ToDay         int64       `json:"to_day"`
	TrackingSince int64       `json:"tracking_since"`
	Totals        Metrics     `json:"totals"`
	Days          []Statistic `json:"days"`
	Members       []Statistic `json:"members"`
	Models        []Statistic `json:"models"`
	Groups        []Statistic `json:"groups"`
	// Keys cover only usage since KeyTrackingSince; earlier usage is attributed to no key.
	Keys             []KeyStatistic `json:"keys"`
	KeyTrackingSince int64          `json:"key_tracking_since"`
	Activity         Activity       `json:"activity"`
	Cost             CostSummary    `json:"cost"`
}

// KeyStatistic carries the owner's username only in workspace-wide statistics.
type KeyStatistic struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Owner          string `json:"owner,omitempty"`
	Revoked        bool   `json:"revoked"`
	Requests       int64  `json:"requests"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	CachedTokens   int64  `json:"cached_tokens"`
	InputReported  int64  `json:"input_reported"`
	OutputReported int64  `json:"output_reported"`
	CachedReported int64  `json:"cached_reported"`
}

// CostSummary is a current-catalog estimate; allowance debits use their saved revision prices.
type CostSummary struct {
	EstimatedMicroUSD int64     `json:"estimated_micro_usd"`
	UnpricedTokens    int64     `json:"unpriced_tokens"`
	Days              []DayCost `json:"days"`
}
type DayCost struct {
	ID                string `json:"id"`
	EstimatedMicroUSD int64  `json:"estimated_micro_usd"`
}

func (s *Service) Statistics(ctx context.Context, days int64) (Statistics, error) {
	return s.statistics(ctx, 0, days)
}
func (s *Service) UserStatistics(ctx context.Context, userID, days int64) (Statistics, error) {
	if userID <= 0 {
		return Statistics{}, accounts.ErrInput
	}
	return s.statistics(ctx, userID, days)
}
func (s *Service) statistics(ctx context.Context, userID, days int64) (Statistics, error) {
	if days != 1 && days != 7 && days != 30 && days != 90 {
		return Statistics{}, accounts.ErrInput
	}
	now := s.now().UTC()
	today := now.Truncate(24 * time.Hour).Unix()
	page := Statistics{FromDay: today - (days-1)*86400, ToDay: today + 86400, Days: []Statistic{}, Members: []Statistic{}, Models: []Statistic{}, Groups: []Statistic{}, Keys: []KeyStatistic{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	// All summaries use one snapshot; concurrent completions cannot make totals and breakdowns disagree.
	page.Activity, err = readActivity(ctx, q, s.tenantID, userID, page.FromDay, page.ToDay, now.Unix())
	if err != nil {
		return page, err
	}
	coverage, err := q.GetStatisticsCoverage(ctx)
	if err != nil {
		return page, err
	}
	// Parse metadata explicitly: SQLite CAST would turn invalid text into a misleading zero.
	page.TrackingSince, err = strconv.ParseInt(coverage, 10, 64)
	if err != nil {
		return page, err
	}
	keyCoverage, err := q.GetKeyStatisticsCoverage(ctx)
	if err != nil {
		return page, err
	}
	if page.KeyTrackingSince, err = strconv.ParseInt(keyCoverage, 10, 64); err != nil {
		return page, err
	}
	totals, err := q.GetStatisticsTotals(ctx, db.GetStatisticsTotalsParams{TenantID: s.tenantID, FromDay: page.FromDay, ToDay: page.ToDay, UserID: userID})
	if err != nil {
		return page, err
	}
	page.Totals = Metrics(totals)
	list := func(dimension string, maxRows int64) ([]Statistic, error) {
		rows, err := q.ListStatistics(ctx, db.ListStatisticsParams{TenantID: s.tenantID, Dimension: dimension, FromDay: page.FromDay, ToDay: page.ToDay, UserID: userID, MaxRows: maxRows})
		if err != nil {
			return nil, err
		}
		values := make([]Statistic, 0, len(rows))
		for _, r := range rows {
			values = append(values, Statistic{ID: r.Bucket, Name: r.Name, Metrics: Metrics{Requests: r.Requests, Completed: r.Completed, Incomplete: r.Incomplete, Errors: r.Errors, Canceled: r.Canceled, Rejected: r.Rejected, DurationMs: r.DurationMs, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CachedTokens: r.CachedTokens, InputReported: r.InputReported, OutputReported: r.OutputReported, CachedReported: r.CachedReported}})
		}
		return values, nil
	}
	daily, err := list("day", 90)
	if err != nil {
		return page, err
	}
	byDay := make(map[string]Statistic, len(daily))
	for _, d := range daily {
		byDay[d.ID] = d
	}
	for day := page.FromDay; day < page.ToDay; day += 86400 {
		id := strconv.FormatInt(day, 10)
		row := byDay[id]
		row.ID = id
		row.Name = time.Unix(day, 0).UTC().Format("2006-01-02")
		page.Days = append(page.Days, row)
	}
	if page.Cost, err = s.estimateCost(ctx, q, userID, page.Days); err != nil {
		return page, err
	}
	if page.Models, err = list("model", 20); err != nil {
		return page, err
	}
	if page.Groups, err = list("group", 20); err != nil {
		return page, err
	}
	keys, err := q.ListKeyStatistics(ctx, db.ListKeyStatisticsParams{TenantID: s.tenantID, FromDay: page.FromDay, ToDay: page.ToDay, UserID: userID})
	if err != nil {
		return page, err
	}
	for _, k := range keys {
		row := KeyStatistic{ID: strconv.FormatInt(k.ID, 10), Name: k.Name, Revoked: k.Revoked != 0, Requests: k.Requests, InputTokens: k.InputTokens, OutputTokens: k.OutputTokens, CachedTokens: k.CachedTokens, InputReported: k.InputReported, OutputReported: k.OutputReported, CachedReported: k.CachedReported}
		if userID == 0 {
			row.Owner = k.Owner
		}
		page.Keys = append(page.Keys, row)
	}
	if userID == 0 {
		if page.Members, err = list("member", 20); err != nil {
			return page, err
		}
	}
	return page, tx.Commit()
}

func (s *Service) estimateCost(ctx context.Context, q *db.Queries, userID int64, days []Statistic) (CostSummary, error) {
	summary := CostSummary{Days: make([]DayCost, 0, len(days))}
	if len(days) == 0 {
		return summary, nil
	}
	from, err := strconv.ParseInt(days[0].ID, 10, 64)
	if err != nil {
		return summary, err
	}
	rows, err := q.ListStatisticsCostBasis(ctx, db.ListStatisticsCostBasisParams{TenantID: s.tenantID, FromDay: from, ToDay: from + int64(len(days))*86400, UserID: userID})
	if err != nil {
		return summary, err
	}
	// Sum exact products per day before rounding; long periods can exceed per-request cost bounds.
	sums := make(map[int64]*big.Int, len(days))
	for _, row := range rows {
		sum := sums[row.Day]
		if sum == nil {
			sum = new(big.Int)
			sums[row.Day] = sum
		}
		summary.UnpricedTokens += s.addCost(sum, costBasis{Model: row.Model, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CachedTokens: row.CachedTokens, InputReported: row.InputReported, CachedReported: row.CachedReported})
	}
	for _, day := range days {
		cost := DayCost{ID: day.ID}
		id, err := strconv.ParseInt(day.ID, 10, 64)
		if err != nil {
			return summary, err
		}
		if sum := sums[id]; sum != nil {
			cost.EstimatedMicroUSD = microUSD(sum)
		}
		summary.EstimatedMicroUSD += cost.EstimatedMicroUSD
		summary.Days = append(summary.Days, cost)
	}
	return summary, nil
}

// KeyUsage is a key's lifetime usage since key tracking began, priced with the current catalog.
type KeyUsage struct {
	Requests          int64 `json:"requests"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	InputReported     int64 `json:"input_reported"`
	OutputReported    int64 `json:"output_reported"`
	EstimatedMicroUSD int64 `json:"estimated_micro_usd"`
	UnpricedTokens    int64 `json:"unpriced_tokens"`
}
type KeyUsageSummary struct {
	TrackingSince int64
	Keys          map[int64]KeyUsage
}

// KeyUsage returns lifetime usage only for the listed keys that userID owns in this workspace.
func (s *Service) KeyUsage(ctx context.Context, userID int64, keyIDs []int64) (KeyUsageSummary, error) {
	summary := KeyUsageSummary{Keys: map[int64]KeyUsage{}}
	if userID <= 0 {
		return summary, accounts.ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return summary, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	coverage, err := q.GetKeyStatisticsCoverage(ctx)
	if err != nil {
		return summary, err
	}
	if summary.TrackingSince, err = strconv.ParseInt(coverage, 10, 64); err != nil {
		return summary, err
	}
	if len(keyIDs) == 0 {
		return summary, tx.Commit()
	}
	rows, err := q.ListKeyLifetimeUsage(ctx, db.ListKeyLifetimeUsageParams{UserID: userID, TenantID: s.tenantID, KeyIds: keyIDs})
	if err != nil {
		return summary, err
	}
	sums := map[int64]*big.Int{}
	for _, row := range rows {
		usage := summary.Keys[row.KeyID]
		usage.Requests += row.Requests
		usage.InputTokens += row.InputTokens
		usage.OutputTokens += row.OutputTokens
		usage.InputReported += row.InputReported
		usage.OutputReported += row.OutputReported
		sum := sums[row.KeyID]
		if sum == nil {
			sum = new(big.Int)
			sums[row.KeyID] = sum
		}
		// Price requests with a cache split exactly; input without one is priceable only when rates match.
		usage.UnpricedTokens += s.addCost(sum, costBasis{Model: row.Model, InputTokens: row.InputTokens - row.UnsplitInputTokens, OutputTokens: row.OutputTokens, CachedTokens: row.CachedTokens})
		usage.UnpricedTokens += s.addCost(sum, costBasis{Model: row.Model, InputTokens: row.UnsplitInputTokens, InputReported: 1})
		summary.Keys[row.KeyID] = usage
	}
	for id, sum := range sums {
		usage := summary.Keys[id]
		usage.EstimatedMicroUSD = microUSD(sum)
		summary.Keys[id] = usage
	}
	return summary, tx.Commit()
}

type costBasis struct {
	Model                                   string
	InputTokens, OutputTokens, CachedTokens int64
	InputReported, CachedReported           int64
}

// addCost adds the row's unrounded current-catalog cost to sum and returns its unpriced tokens.
func (s *Service) addCost(sum *big.Int, row costBasis) int64 {
	price, ok := pricing.Price{}, false
	if s.pricing != nil {
		price, ok = s.pricing.Lookup(row.Model)
	}
	// Overflow labels and partially reported cache splits cannot be priced when rates differ.
	if !ok || (row.CachedReported < row.InputReported && price.Input != price.Cached) || row.CachedTokens > row.InputTokens {
		return row.InputTokens + row.OutputTokens
	}
	for _, v := range [][2]int64{{row.InputTokens - row.CachedTokens, price.Input}, {row.CachedTokens, price.Cached}, {row.OutputTokens, price.Output}} {
		sum.Add(sum, new(big.Int).Mul(big.NewInt(v[0]), big.NewInt(v[1])))
	}
	return 0
}

// microUSD rounds an exact sum up so that any priced usage is never shown as free.
func microUSD(sum *big.Int) int64 {
	rounded := new(big.Int).Add(sum, big.NewInt(999_999))
	return rounded.Div(rounded, big.NewInt(1_000_000)).Int64()
}

func recordKeyLifetimeUsage(ctx context.Context, q *db.Queries, keyID int64, model string, entry db.RecordStatisticsParams) error {
	// Lifetime rows are never pruned, so each key keeps its own bound on model labels.
	tracked, err := q.CanTrackKeyModel(ctx, db.CanTrackKeyModelParams{KeyID: keyID, Model: model})
	if err != nil {
		return err
	}
	if tracked == 0 {
		model = "[other models]"
	}
	usage := db.RecordKeyLifetimeUsageParams{KeyID: keyID, Model: model, Requests: entry.Requests, InputTokens: entry.InputTokens, OutputTokens: entry.OutputTokens, CachedTokens: entry.CachedTokens, InputReported: entry.InputReported, OutputReported: entry.OutputReported}
	if entry.InputReported == 1 && entry.CachedReported == 0 {
		usage.UnsplitInputTokens = entry.InputTokens
	}
	return q.RecordKeyLifetimeUsage(ctx, usage)
}

func recordStatistics(ctx context.Context, q *db.Queries, r db.RecordRequestParams) error {
	if r.UserID <= 0 {
		return nil
	}
	model := r.Model
	if r.Provider != "" && model != "" {
		model = r.Provider + "/" + strings.TrimPrefix(model, r.Provider+"/")
	}
	// Model names are client-controlled. Bound new labels while retaining every request in totals.
	tracked, err := q.CanTrackStatisticsModel(ctx, db.CanTrackStatisticsModelParams{GroupID: r.GroupID, Day: r.StartedAt - r.StartedAt%86400, UserID: r.UserID, Model: model})
	if err != nil {
		return err
	}
	if tracked == 0 {
		model = "[other models]"
	}
	entry := db.RecordStatisticsParams{Day: r.StartedAt - r.StartedAt%86400, UserID: r.UserID, GroupID: r.GroupID, Provider: r.Provider, Model: model, Requests: 1, DurationMs: r.DurationMs}
	switch r.Outcome {
	case "success":
		entry.Completed = 1
		// Completion survives bounded history cleanup and commits with the request's accounting.
		if err := q.RecordFirstRequest(ctx, db.RecordFirstRequestParams{UserID: r.UserID, GroupID: r.GroupID, StartedAt: r.StartedAt}); err != nil {
			return err
		}
	case "incomplete":
		entry.Incomplete = 1
	case "error":
		entry.Errors = 1
	case "canceled":
		entry.Canceled = 1
	case "rejected":
		entry.Rejected = 1
	}
	if r.InputTokens != nil {
		entry.InputTokens = *r.InputTokens
		entry.InputReported = 1
	}
	if r.OutputTokens != nil {
		entry.OutputTokens = *r.OutputTokens
		entry.OutputReported = 1
	}
	if r.CachedTokens != nil {
		entry.CachedTokens = *r.CachedTokens
		entry.CachedReported = 1
	}
	if err := q.RecordStatistics(ctx, entry); err != nil {
		return err
	}
	if r.KeyID > 0 {
		if err := q.RecordKeyStatistics(ctx, db.RecordKeyStatisticsParams{Day: entry.Day, KeyID: r.KeyID, Requests: entry.Requests, InputTokens: entry.InputTokens, OutputTokens: entry.OutputTokens, CachedTokens: entry.CachedTokens, InputReported: entry.InputReported, OutputReported: entry.OutputReported, CachedReported: entry.CachedReported}); err != nil {
			return err
		}
		if err := recordKeyLifetimeUsage(ctx, q, r.KeyID, model, entry); err != nil {
			return err
		}
	}
	return q.RecordHourlyUsage(ctx, db.RecordHourlyUsageParams{GroupID: r.GroupID, Hour: r.StartedAt - r.StartedAt%3600, UserID: r.UserID, Requests: entry.Requests, InputTokens: entry.InputTokens, OutputTokens: entry.OutputTokens, InputReported: entry.InputReported, OutputReported: entry.OutputReported})
}
