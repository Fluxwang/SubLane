package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/storage"
	"github.com/murongg/SubLane/internal/vault"
)

func TestCatalogPersistsAndRejectsObsoleteAuthorization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	connection, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedInitialTenant(t, connection)
	cipher, err := vault.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	s := New(connection, cipher)
	credential := Credential{AccountID: "synthetic-catalog", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	a, err := s.Authorize(ctx, "Synthetic", credential, "")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Catalog(ctx, a.ID)
	if err != nil || initial.UpdatedAt != 0 || initial.Models == nil {
		t.Fatal("new account must be unknown", initial, err)
	}
	at := time.Now().Unix()
	if err := s.SaveCatalog(ctx, a.ID, initial.Revision, []string{"synthetic-z", "synthetic-a", "synthetic-z"}, nil, at, "synthetic:v1"); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	connection, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	s = New(connection, cipher)
	saved, err := s.Catalog(ctx, a.ID)
	if err != nil || saved.UpdatedAt != at || saved.Source != "synthetic:v1" || !reflect.DeepEqual(saved.Models, []string{"synthetic-a", "synthetic-z"}) {
		t.Fatal("snapshot did not survive restart", saved, err)
	}
	if _, err := s.Authorize(ctx, "Synthetic", credential, a.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.Catalog(ctx, a.ID)
	if err != nil || current.UpdatedAt != 0 || current.Revision == saved.Revision {
		t.Fatal("reauthorization retained old capabilities", current, err)
	}
	if err := s.SaveCatalog(ctx, a.ID, saved.Revision, saved.Models, nil, at+1, "synthetic:v1"); !errors.Is(err, ErrCatalogChanged) {
		t.Fatal("obsolete fetch published", err)
	}
	if err := s.SaveCatalog(ctx, a.ID, current.Revision, []string{}, nil, at, "synthetic:v1"); err != nil {
		t.Fatal(err)
	}
	empty, err := s.Catalog(ctx, a.ID)
	if err != nil || empty.UpdatedAt == 0 || len(empty.Models) != 0 {
		t.Fatal("known empty catalog became unknown", empty, err)
	}
	if _, err := s.SetEnabled(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCatalog(ctx, a.ID, current.Revision, saved.Models, nil, at, "synthetic:v1"); !errors.Is(err, ErrCatalogChanged) {
		t.Fatal("disable/re-enable allowed an old fetch", err)
	}
	if err := s.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Catalog(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted snapshot remained", err)
	}
}

func TestCatalogRejectsInvalidModelsWithoutReplacingSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	connection, err := storage.Open(ctx, filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedInitialTenant(t, connection)
	cipher, err := vault.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	s := New(connection, cipher)
	a, err := s.Authorize(ctx, "Synthetic", Credential{AccountID: "synthetic", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}, "")
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := s.Catalog(ctx, a.ID)
	for _, models := range [][]string{{"bad\nmodel"}, {""}, make([]string, 513)} {
		if err := s.SaveCatalog(ctx, a.ID, catalog.Revision, models, nil, time.Now().Unix(), "synthetic:v1"); !errors.Is(err, ErrInput) {
			t.Fatal("invalid catalog accepted", err)
		}
	}
	catalog, err = s.Catalog(ctx, a.ID)
	if err != nil || catalog.UpdatedAt != 0 {
		t.Fatal("invalid catalog published", err)
	}
}

func TestCatalogDecodesLegacySnapshotsWithoutHiddenModels(t *testing.T) {
	value, err := DecodeCatalog([]byte(`{"models":["synthetic-visible"],"updated_at":1}`), 1)
	if err != nil || value.HiddenModels == nil || len(value.HiddenModels) != 0 || !value.Contains("synthetic-visible") {
		t.Fatal("legacy catalog snapshot changed meaning", value, err)
	}
	value, err = DecodeCatalog([]byte(`{"models":["synthetic-visible"],"hidden_models":["synthetic-visible","synthetic-internal"],"updated_at":1}`), 1)
	if err != nil || len(value.HiddenModels) != 1 || !value.Contains("synthetic-internal") {
		t.Fatal("hidden catalog entries were not normalized", value, err)
	}
}
