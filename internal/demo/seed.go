package demo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/apikey"
	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage/db"
	"github.com/murongg/SubLane/internal/upstream"
)

func seed(ctx context.Context, connection *sql.DB, identity *auth.Service, accountService *accounts.Service, pools *groups.Service, keys *apikey.Service) error {
	login, err := identity.Setup(ctx, Username, Password, "Demo workspace")
	if err != nil {
		return err
	}
	if err := identity.Revoke(ctx, login.Token); err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Actor{TenantID: 1, ID: 1, Username: Username, Role: "admin", Source: "user"})
	now := time.Now().UTC()
	var accountIDs []string
	for i, name := range []string{"Demo · Development", "Demo · Research", "Demo · Shared"} {
		account, err := accountService.Authorize(ctx, name, accounts.Credential{
			AccountID: fmt.Sprintf("demo-account-%d", i+1), Email: fmt.Sprintf("demo-%d@example.test", i+1), Plan: "plus",
			AccessToken: "synthetic-demo-access", RefreshToken: "synthetic-demo-refresh", ExpiresAt: now.AddDate(10, 0, 0).Unix(),
		}, "")
		if err != nil {
			return err
		}
		if err := accountService.SaveCatalog(ctx, account.ID, 0, []string{"demo-codex", "demo-codex-mini"}, nil, now.Unix(), upstream.CatalogSource("codex")); err != nil {
			return err
		}
		accountIDs = append(accountIDs, account.ID)
	}
	var groupIDs []int64
	for i, name := range []string{"Demo · Engineering", "Demo · Sandbox"} {
		pool, err := pools.Save(ctx, 0, groups.Input{Name: name, Enabled: true, AccountIDs: accountIDs[i : i+2]})
		if err != nil {
			return err
		}
		groupIDs = append(groupIDs, pool.ID)
	}
	users := []int64{1}
	for _, name := range []string{"demo-developer", "demo-designer"} {
		member, err := identity.CreateMember(ctx, name, Password)
		if err != nil {
			return err
		}
		if err := pools.SetMemberGroups(ctx, member.ID, groupIDs); err != nil {
			return err
		}
		users = append(users, member.ID)
	}
	var keyIDs []int64
	for i, user := range users {
		key, err := keys.CreateInGroup(ctx, user, groupIDs[i%2], "Demo · Local client")
		if err != nil {
			return err
		}
		keyIDs = append(keyIDs, key.Key.ID)
	}
	// Derive history, daily totals, and the heatmap from the same synthetic events.
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(connection).WithTx(tx)
	start := now.Truncate(24*time.Hour).AddDate(0, 0, -29)
	if err := q.SetDemoCoverage(ctx, strconv.FormatInt(start.Unix(), 10)); err != nil {
		return err
	}
	for day := 0; day < 30; day++ {
		for index := 0; index < 18+day%9; index++ {
			at := start.AddDate(0, 0, day).Add(time.Duration(8+index%12)*time.Hour + time.Duration(index)*time.Minute)
			if at.After(now) {
				continue
			}
			userIndex := index % len(users)
			groupID := groupIDs[userIndex%2]
			model := []string{"demo-codex", "demo-codex-mini"}[index%2]
			input, output, cached, first := int64(1200+index*127), int64(250+index*31), int64(600+index*35), int64(320+index*7)
			record := db.RecordRequestParams{
				UserID: users[userIndex], KeyID: keyIDs[userIndex], GroupID: groupID, AccountID: accountIDs[userIndex%2], Provider: "codex", Model: model,
				Transport: "http", Operation: "responses", StartedAt: at.Unix(), DurationMs: 1600 + int64(index)*91,
				Outcome: "success", InputTokens: &input, OutputTokens: &output, CachedTokens: &cached, FirstTokenMs: &first,
				RequestID: fmt.Sprintf("req_demo_%02d_%02d", day, index), ReasoningEffort: "medium",
			}
			metrics := db.RecordStatisticsParams{Day: at.Truncate(24 * time.Hour).Unix(), UserID: record.UserID, GroupID: groupID, Provider: "codex", Model: model,
				Requests: 1, Completed: 1, DurationMs: record.DurationMs, InputTokens: input, OutputTokens: output, CachedTokens: cached, InputReported: 1, OutputReported: 1, CachedReported: 1}
			if index%13 == 12 {
				record.Outcome, record.ErrorCode = "error", "upstream_error"
				record.InputTokens, record.OutputTokens, record.CachedTokens, record.FirstTokenMs = nil, nil, nil, nil
				metrics.Completed, metrics.Errors = 0, 1
				metrics.InputTokens, metrics.OutputTokens, metrics.CachedTokens = 0, 0, 0
				metrics.InputReported, metrics.OutputReported, metrics.CachedReported = 0, 0, 0
			}
			if err := q.RecordRequest(ctx, record); err != nil {
				return err
			}
			if err := q.RecordStatistics(ctx, metrics); err != nil {
				return err
			}
			if err := q.RecordKeyStatistics(ctx, db.RecordKeyStatisticsParams{Day: metrics.Day, KeyID: record.KeyID, Requests: 1, InputTokens: metrics.InputTokens, OutputTokens: metrics.OutputTokens,
				CachedTokens: metrics.CachedTokens, InputReported: metrics.InputReported, OutputReported: metrics.OutputReported, CachedReported: metrics.CachedReported}); err != nil {
				return err
			}
			if err := q.RecordHourlyUsage(ctx, db.RecordHourlyUsageParams{GroupID: groupID, Hour: at.Truncate(time.Hour).Unix(), UserID: record.UserID,
				Requests: 1, InputTokens: metrics.InputTokens, OutputTokens: metrics.OutputTokens, InputReported: metrics.InputReported, OutputReported: metrics.OutputReported}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
