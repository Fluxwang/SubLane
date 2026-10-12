// Package versions owns instance-wide client version policies and release-check lifecycles.
package versions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/storage/db"
)

const syncInterval = 6 * time.Hour
const checkTimeout = 30 * time.Second
const manualCooldown = time.Minute

var ErrInput = errors.New("invalid_client_version")
var ErrSync = errors.New("client_version_sync_failed")
var ErrCooldown = errors.New("client_version_sync_cooldown")
var ErrStorage = errors.New("client_version_storage_failed")
var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$`)

type Config struct {
	ManualVersion string `json:"manual_version"`
	AutoSync      bool   `json:"auto_sync"`
}
type record struct {
	Config
	LatestVersion string `json:"latest_version"`
	CheckedAt     int64  `json:"checked_at"`
	SuccessfulAt  int64  `json:"successful_at"`
	NextCheckAt   int64  `json:"next_check_at"`
	RetryAt       int64  `json:"retry_at"`
	SyncFailed    bool   `json:"sync_failed"`
}
type State struct {
	Config
	EffectiveVersion  string `json:"effective_version"`
	DefaultVersion    string `json:"default_version"`
	LatestVersion     string `json:"latest_version"`
	Source            string `json:"source"`
	CheckedAt         int64  `json:"checked_at"`
	SuccessfulAt      int64  `json:"successful_at"`
	NextCheckAt       int64  `json:"next_check_at"`
	ServerTime        int64  `json:"server_time"`
	RetryAfterSeconds int64  `json:"retry_after_seconds"`
	Syncing           bool   `json:"syncing"`
	SyncFailed        bool   `json:"sync_failed"`
}
type Fetcher func(context.Context) (string, error)
type Provider string

const (
	Codex Provider = "codex"
	Grok  Provider = "grok"
)

type operation struct {
	done       chan struct{}
	cancel     context.CancelFunc
	manual     bool
	generation uint64
	err        error
}
type Service struct {
	provider        Provider
	connection      *sql.DB
	queries         *db.Queries
	fallback        string
	fetch           Fetcher
	now             func() time.Time
	current         atomic.Value
	mu              sync.Mutex
	state           record
	flight          *operation
	ctx             context.Context
	cancel          context.CancelFunc
	wake            chan struct{}
	workers         sync.WaitGroup
	generation      uint64
	started, closed bool
	storageFailed   bool
	runtimeRetryAt  int64
}

func normalize(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" && !stableVersion.MatchString(value) {
		return "", ErrInput
	}
	return value, nil
}
func compare(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		av, _ := strconv.Atoi(left[i])
		bv, _ := strconv.Atoi(right[i])
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
func New(parent context.Context, connection *sql.DB, fallback string, fetch Fetcher) (*Service, error) {
	return NewForProvider(parent, connection, Codex, fallback, fetch)
}

func NewForProvider(parent context.Context, connection *sql.DB, provider Provider, fallback string, fetch Fetcher) (*Service, error) {
	if provider != Codex && provider != Grok {
		return nil, ErrInput
	}
	fallback, err := normalize(fallback)
	if err != nil || fallback == "" || fetch == nil {
		return nil, ErrInput
	}
	value := record{Config: Config{AutoSync: true}}
	queries := db.New(connection)
	// Keep the existing Codex key so upgrades preserve manual pins and synchronization history.
	raw, err := queries.GetClientVersionState(parent, string(provider)+".version")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStorage
	}
	if err == nil {
		if len(raw) > 4096 || json.Unmarshal([]byte(raw), &value) != nil {
			return nil, ErrInput
		}
		if value.ManualVersion, err = normalize(value.ManualVersion); err != nil {
			return nil, err
		}
		if value.LatestVersion, err = normalize(value.LatestVersion); err != nil {
			return nil, err
		}
		if value.CheckedAt < 0 || value.SuccessfulAt < 0 || value.NextCheckAt < 0 || value.RetryAt < 0 {
			return nil, ErrInput
		}
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{provider: provider, connection: connection, queries: queries, fallback: fallback, fetch: fetch, now: time.Now, state: value, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	effective, _ := s.effective(value)
	s.current.Store(effective)
	return s, nil
}
func (s *Service) effective(value record) (string, string) {
	if value.ManualVersion != "" {
		return value.ManualVersion, "manual"
	}
	if value.LatestVersion != "" && compare(value.LatestVersion, s.fallback) >= 0 {
		return value.LatestVersion, "synced"
	}
	return s.fallback, "builtin"
}
func (s *Service) Current() string { return s.current.Load().(string) }
func (s *Service) view() State {
	value := s.state
	effective, source := s.effective(value)
	next := value.NextCheckAt
	if !value.AutoSync {
		next = 0
	}
	if value.AutoSync {
		next = max(next, s.runtimeRetryAt)
	}
	return State{Config: value.Config, EffectiveVersion: effective, DefaultVersion: s.fallback, LatestVersion: value.LatestVersion, Source: source, CheckedAt: value.CheckedAt, SuccessfulAt: value.SuccessfulAt, NextCheckAt: next, ServerTime: s.now().Unix(), RetryAfterSeconds: max(0, int64(math.Ceil(time.Unix(max(value.RetryAt, s.runtimeRetryAt), 0).Sub(s.now()).Seconds()))), Syncing: s.flight != nil, SyncFailed: value.SyncFailed || s.storageFailed}
}
func (s *Service) View() State { s.mu.Lock(); defer s.mu.Unlock(); return s.view() }

// persist runs only under mu; publication follows both the settings write and its audit commit.
func (s *Service) persist(ctx context.Context, value record, manual bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return ErrStorage
	}
	tx, err := s.connection.BeginTx(ctx, nil)
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	if err := q.SaveClientVersionState(ctx, db.SaveClientVersionStateParams{Key: string(s.provider) + ".version", Value: string(raw)}); err != nil {
		return ErrStorage
	}
	if manual {
		if err := audit.Record(ctx, q, "settings.update", "settings", string(s.provider)); err != nil {
			return ErrStorage
		}
	}
	if err := tx.Commit(); err != nil {
		return ErrStorage
	}
	s.state = value
	s.storageFailed = false
	s.runtimeRetryAt = 0
	version, _ := s.effective(value)
	s.current.Store(version)
	return nil
}
func (s *Service) Save(ctx context.Context, config Config) (State, error) {
	manual, err := normalize(config.ManualVersion)
	if err != nil {
		return State{}, err
	}
	config.ManualVersion = manual
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return s.view(), context.Canceled
	}
	if config == s.state.Config {
		return s.view(), nil
	}
	value := s.state
	value.Config = config
	if err := s.persist(ctx, value, true); err != nil {
		return s.view(), err
	}
	if !config.AutoSync && s.flight != nil && !s.flight.manual {
		s.generation++
		s.flight.cancel()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return s.view(), nil
}
func (s *Service) Sync(ctx context.Context, manual bool) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return State{}, context.Canceled
	}
	if !manual && (!s.state.AutoSync || s.now().Unix() < s.state.NextCheckAt) {
		state := s.view()
		s.mu.Unlock()
		return state, nil
	}
	op := s.flight
	if op == nil {
		if s.now().Unix() < max(s.state.RetryAt, s.runtimeRetryAt) {
			state := s.view()
			s.mu.Unlock()
			return state, ErrCooldown
		}
		shared, cancel := context.WithTimeout(s.ctx, checkTimeout)
		op = &operation{done: make(chan struct{}), cancel: cancel, manual: manual, generation: s.generation}
		s.flight = op
		s.workers.Add(1)
		go s.check(shared, op)
	}
	s.mu.Unlock()
	// Cancellation belongs to each waiter. Shared checks are canceled by service shutdown or an explicit disable.
	select {
	case <-ctx.Done():
		return State{}, ctx.Err()
	case <-op.done:
	}
	return s.View(), op.err
}
func (s *Service) check(ctx context.Context, op *operation) {
	defer s.workers.Done()
	defer op.cancel()
	latest, err := s.fetch(ctx)
	if err == nil {
		latest, err = normalize(latest)
		if latest == "" {
			err = ErrSync
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.flight = nil; close(op.done) }()
	if s.closed || s.ctx.Err() != nil {
		op.err = context.Canceled
		return
	}
	if !op.manual && (!s.state.AutoSync || op.generation != s.generation) {
		return
	}
	// Merge into the current configuration, never the one captured before the network request.
	value := s.state
	now := s.now()
	value.CheckedAt = now.Unix()
	value.RetryAt = now.Add(manualCooldown).Unix()
	value.SyncFailed = err != nil
	if err != nil {
		value.NextCheckAt = now.Add(15 * time.Minute).Unix()
		var limited *rateLimit
		if errors.As(err, &limited) {
			value.RetryAt = max(value.RetryAt, now.Add(limited.retry).Unix())
			value.NextCheckAt = max(value.NextCheckAt, value.RetryAt)
		}
		op.err = ErrSync
	} else {
		if value.LatestVersion == "" || compare(latest, value.LatestVersion) > 0 {
			value.LatestVersion = latest
		}
		value.SuccessfulAt = now.Unix()
		value.NextCheckAt = now.Add(syncInterval).Unix()
	}
	if err := s.persist(s.ctx, value, false); err != nil {
		s.storageFailed = true
		// Keep a process-local backoff even when SQLite cannot persist the attempt.
		s.runtimeRetryAt = now.Add(15 * time.Minute).Unix()
		op.err = err
	}
}
func (s *Service) Start() {
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			_, _ = s.Sync(s.ctx, false)
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
}
func (s *Service) Close() { s.mu.Lock(); s.closed = true; s.cancel(); s.mu.Unlock(); s.workers.Wait() }
