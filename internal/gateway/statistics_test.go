package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/murongg/SubLane/internal/pricing"
	"github.com/murongg/SubLane/internal/storage/db"
)

func TestStatisticsAreDurableIndependentAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return syntheticStream(), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[]}}\n\n"))}, nil
	}))
	member := runtimeMember(t, s)
	for _, id := range []int64{1, member} {
		r, err := s.Open(ctx, id, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Events(func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		r.Body.Close()
	}
	if _, err := s.db.Exec("DELETE FROM request_records"); err != nil {
		t.Fatal(err)
	}
	restarted := New(ctx, s.db, s.accounts, s.provider)
	defer restarted.Close()
	all, err := restarted.Statistics(ctx, 7)
	if err != nil || all.Totals.Requests != 2 || all.Totals.Completed != 2 || all.Totals.InputTokens != 3 || all.Totals.InputReported != 1 || len(all.Days) != 7 || len(all.Members) != 2 {
		t.Fatal("aggregates lost or inaccurate", all, err)
	}
	mine, err := restarted.UserStatistics(ctx, member, 7)
	if err != nil || mine.Totals.Requests != 1 || mine.Totals.InputReported != 0 || len(mine.Members) != 0 {
		t.Fatal("personal statistics leaked or fabricated tokens", mine, err)
	}
	if len(mine.Models) != 1 || mine.Models[0].Name != "codex/synthetic-model" || len(mine.Groups) != 1 {
		t.Fatal("missing breakdowns", mine)
	}
	var callsAfterRestart int64
	for _, cell := range mine.Activity.Cells {
		callsAfterRestart += cell.Requests
	}
	if callsAfterRestart != 1 {
		t.Fatal("hourly totals did not survive history cleanup and restart")
	}
	if _, err := s.UserStatistics(ctx, 0, 7); err == nil {
		t.Fatal("missing identity expanded access")
	}
	if _, err := s.Statistics(ctx, 365); err == nil {
		t.Fatal("unbounded period")
	}
}

func TestStatisticsShareHistoryTransactionAndRetainOnlyNinetyDays(t *testing.T) {
	ctx := context.Background()
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
	if _, err := s.db.Exec("CREATE TRIGGER fail_statistics BEFORE INSERT ON usage_daily BEGIN SELECT RAISE(ABORT,'synthetic failure'); END"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	history, err := s.Requests(ctx, RequestFilter{})
	if err != nil || len(history.Requests) != 0 {
		t.Fatal("history committed without aggregate", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_statistics"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := db.RecordRequestParams{UserID: 1, GroupID: 1, Model: "synthetic", StartedAt: s.now().Unix() - 91*86400, Outcome: "success"}
	if err := recordStatistics(ctx, s.queries.WithTx(tx), old); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	value, err := s.Statistics(ctx, 90)
	if err != nil || value.Totals.Requests != 0 {
		t.Fatal("old aggregate retained", value, err)
	}
	var rows int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM usage_daily").Scan(&rows); err != nil || rows != 1 {
		t.Fatal("statistics read changed stored rows", rows, err)
	}
	if err := pruneHistory(ctx, s.db, s.now()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM usage_daily").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("expired rows not reclaimed", rows, err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM usage_hourly").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("expired hourly rows not reclaimed", rows, err)
	}
}

func TestStatisticsBoundsModelCardinalityWithoutLosingCounts(t *testing.T) {
	ctx := context.Background()
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected upstream"); return nil, nil }))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 70 {
		record := db.RecordRequestParams{UserID: 1, GroupID: 1, Provider: "codex", Model: fmt.Sprintf("synthetic-%d", i), StartedAt: s.now().Unix(), Outcome: "rejected"}
		if err := recordStatistics(ctx, s.queries.WithTx(tx), record); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var rows, overflow int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM usage_daily").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COALESCE(SUM(requests),0) FROM usage_daily WHERE model='[other models]'").Scan(&overflow); err != nil {
		t.Fatal(err)
	}
	if rows != 65 || overflow != 6 {
		t.Fatal("unbounded model labels or lost requests", rows, overflow)
	}
	stats, err := s.Statistics(ctx, 1)
	if err != nil || stats.Totals.Requests != 70 {
		t.Fatal("overflow lost totals", stats, err)
	}
}

func TestStatisticsRejectsInvalidCoverageSettings(t *testing.T) {
	for _, key := range []string{"usage.daily.started_at", "usage.hourly.started_at"} {
		for _, value := range []string{"not-a-time", "9223372036854775808"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				service, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected upstream"); return nil, nil }))
				if _, err := service.db.Exec("UPDATE settings SET value=? WHERE key=?", value, key); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Statistics(context.Background(), 7); err == nil {
					t.Fatal("corrupt coverage silently treated as a timestamp")
				}
			})
		}
	}
}

func TestStatisticsEstimateCostFromCurrentPricesAndReportUnpricedTokens(t *testing.T) {
	ctx := context.Background()
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected upstream"); return nil, nil }))
	// Rates are micro-USD per million tokens.
	s.pricing = pricing.NewStatic(map[string]pricing.Price{"synthetic-priced": {Input: 2_000_000, Cached: 500_000, Output: 8_000_000}})
	value := func(v int64) *int64 { return &v }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, record := range []db.RecordRequestParams{
		{Model: "synthetic-priced", InputTokens: value(1_000_000), OutputTokens: value(100_000), CachedTokens: value(400_000)},
		{Model: "synthetic-unpriced", InputTokens: value(300), OutputTokens: value(50), CachedTokens: value(0)},
		// Without a cache split, differing rates make the price of this input unknown.
		// Daily aggregates merge a model's requests, so this case uses the previous day.
		{Model: "synthetic-priced", InputTokens: value(1_000), OutputTokens: value(0), StartedAt: s.now().Unix() - 86400},
	} {
		if record.StartedAt == 0 {
			record.StartedAt = s.now().Unix()
		}
		record.UserID, record.GroupID, record.Provider, record.Outcome = 1, 1, "codex", "success"
		if err := recordStatistics(ctx, s.queries.WithTx(tx), record); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Statistics(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cost.EstimatedMicroUSD != 2_200_000 || stats.Cost.UnpricedTokens != 1_350 {
		t.Fatal("inaccurate cost estimate", stats.Cost)
	}
	if len(stats.Cost.Days) != len(stats.Days) || stats.Cost.Days[6].ID != stats.Days[6].ID || stats.Cost.Days[6].EstimatedMicroUSD != 2_200_000 || stats.Cost.Days[5].EstimatedMicroUSD != 0 {
		t.Fatal("daily costs misaligned", stats.Cost.Days)
	}
	s.pricing = nil
	if stats, err = s.Statistics(ctx, 7); err != nil || stats.Cost.EstimatedMicroUSD != 0 || stats.Cost.UnpricedTokens != 1_101_350 {
		t.Fatal("missing price catalog fabricated cost", stats.Cost, err)
	}
}

func TestStatisticsRankModelsByReportedTokens(t *testing.T) {
	ctx := context.Background()
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected upstream"); return nil, nil }))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tokens := func(v int64) *int64 { return &v }
	record := func(model string, input, output int64) {
		r := db.RecordRequestParams{UserID: 1, GroupID: 1, Provider: "codex", Model: model, StartedAt: s.now().Unix(), Outcome: "success", InputTokens: tokens(input), OutputTokens: tokens(output)}
		if err := recordStatistics(ctx, s.queries.WithTx(tx), r); err != nil {
			t.Fatal(err)
		}
	}
	// Twenty request-heavy models would fill the old request-ordered cutoff.
	for i := range 20 {
		for range 3 {
			record(fmt.Sprintf("busy-%02d", i), 10, 10)
		}
	}
	record("long-context", 500_000, 1_000)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Statistics(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Models) != 20 || stats.Models[0].Name != "codex/long-context" {
		t.Fatal("token-heavy model missing from the token ranking", stats.Models)
	}
}
