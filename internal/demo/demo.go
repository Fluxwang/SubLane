// Package demo owns the disposable, offline demonstration runtime.
package demo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/alerts"
	"github.com/murongg/SubLane/internal/apikey"
	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/content"
	"github.com/murongg/SubLane/internal/gateway"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/pricing"
	"github.com/murongg/SubLane/internal/server"
	"github.com/murongg/SubLane/internal/storage"
	"github.com/murongg/SubLane/internal/tenants"
	"github.com/murongg/SubLane/internal/timezone"
	"github.com/murongg/SubLane/internal/upstream"
	"github.com/murongg/SubLane/internal/vault"
	"github.com/murongg/SubLane/internal/versions"
)

const Username = "demo"
const Password = "sublane-demo"

type Options struct {
	Assets         fs.FS
	Version        string
	PublicURL      string
	TrustedProxies []netip.Prefix
}

type Instance struct {
	Handler      http.Handler
	directory    string
	connection   *sql.DB
	gateway      *gateway.Service
	provider     *upstream.Client
	versions     *versions.Service
	grokVersions *versions.Service
	once         sync.Once
	closeErr     error
}

func New(ctx context.Context, options Options) (_ *Instance, err error) {
	// Never accept a data path: even an existing SUBLANE_DATA_DIR cannot be opened in demo mode.
	directory, err := os.MkdirTemp("", "sublane-demo-")
	if err != nil {
		return nil, err
	}
	instance := &Instance{directory: directory}
	defer func() {
		if err != nil {
			_ = instance.Close()
		}
	}()
	connection, err := storage.Open(ctx, filepath.Join(directory, "sublane.db"))
	if err != nil {
		return nil, err
	}
	instance.connection = connection
	cipher, err := vault.Open(filepath.Join(directory, "vault.key"), true)
	if err != nil {
		return nil, err
	}
	identity, err := auth.New(connection)
	if err != nil {
		return nil, err
	}
	accountService := accounts.New(connection, cipher)
	accountService.RestrictToCodex()
	poolService := groups.New(connection)
	keys := apikey.New(connection, cipher)
	if err = seed(ctx, connection, identity, accountService, poolService, keys); err != nil {
		return nil, err
	}
	zone, err := timezone.New(ctx, connection)
	if err != nil {
		return nil, err
	}
	// No SDK runtime, release poller, price fetcher, or real network transport is started.
	provider := upstream.NewWithTransport(transport{})
	instance.provider = provider
	forwarding := gateway.New(ctx, connection, accountService, provider)
	forwarding.SetTimeZone(zone)
	instance.gateway = forwarding
	versionPolicy, err := versions.New(ctx, connection, upstream.DefaultCodexVersion, func(context.Context) (string, error) { return "", errors.New("demo_read_only") })
	if err != nil {
		return nil, err
	}
	instance.versions = versionPolicy
	if _, err = versionPolicy.Save(ctx, versions.Config{AutoSync: false}); err != nil {
		return nil, err
	}
	grokPolicy, err := versions.NewForProvider(ctx, connection, versions.Grok, upstream.DefaultGrokVersion, func(context.Context) (string, error) { return "", errors.New("demo_read_only") })
	if err != nil {
		return nil, err
	}
	instance.grokVersions = grokPolicy
	if _, err = grokPolicy.Save(ctx, versions.Config{AutoSync: false}); err != nil {
		return nil, err
	}
	tenancy := tenants.New(connection)
	handler := server.New(server.Options{
		Demo:    &server.DemoCredentials{Username: Username, Password: Password},
		DataDir: directory, Assets: options.Assets, Version: options.Version, StartedAt: time.Now(),
		Ping: connection.PingContext, Audit: audit.NewForTenant(connection, 1), Auth: identity,
		Keys: keys, Accounts: accountService, Gateway: forwarding, Groups: poolService,
		Pricing: pricing.NewStatic(nil),
		Tenants: tenancy, TenantID: 1, PublicURL: options.PublicURL, TrustedProxies: options.TrustedProxies,
		CodexVersions: versionPolicy, GrokVersions: grokPolicy, TimeZone: zone,
		// Expose configuration reads without starting the outbound notification worker.
		Alerts:  alerts.New(connection, cipher, nil),
		Content: content.New(connection, cipher, 1),
	})
	instance.Handler = readOnly(server.NewMulti(connection, identity, tenancy, options.PublicURL, func(int64) http.Handler { return handler }))
	return instance, nil
}

func (i *Instance) Close() error {
	i.once.Do(func() {
		if i.gateway != nil {
			i.gateway.Close()
		}
		if i.provider != nil {
			i.provider.Close()
		}
		if i.versions != nil {
			i.versions.Close()
		}
		if i.grokVersions != nil {
			i.grokVersions.Close()
		}
		if i.connection != nil {
			i.closeErr = i.connection.Close()
		}
		i.closeErr = errors.Join(i.closeErr, os.RemoveAll(i.directory))
	})
	return i.closeErr
}

func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		gateway := path == "/v1" || strings.HasPrefix(path, "/v1/") || path == "/v1beta" || strings.HasPrefix(path, "/v1beta/") || path == "/v0" || strings.HasPrefix(path, "/v0/")
		login := r.Method == http.MethodPost && (path == "/api/auth/login" || path == "/api/auth/logout")
		// Apply outside workspace routing so global writes and WebSocket GET upgrades are also blocked.
		if gateway || (!login && r.Method != http.MethodGet && r.Method != http.MethodHead) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "demo_read_only"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
