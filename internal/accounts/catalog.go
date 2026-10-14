package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"

	"github.com/murongg/SubLane/internal/storage/db"
)

var ErrCatalogChanged = errors.New("account_catalog_changed")

var catalogModelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/()+-]{0,127}$`)

// UpdatedAt zero means unknown; an observed empty list is a known catalog.
type Catalog struct {
	Models []string `json:"models"`
	// HiddenModels are routable but never listed in public catalogs.
	HiddenModels []string `json:"hidden_models"`
	UpdatedAt    int64    `json:"updated_at"`
	Source       string   `json:"source,omitempty"`
	Revision     int64    `json:"-"`
}

func (s *Service) Catalog(ctx context.Context, id string) (Catalog, error) {
	if _, err := s.get(ctx, id); err != nil {
		return Catalog{}, err
	}
	row, err := s.queries.GetAccountCatalog(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Catalog{}, ErrNotFound
	}
	if err != nil {
		return Catalog{}, err
	}
	return DecodeCatalog(row.ModelsSnapshot, row.ModelsRevision)
}

func DecodeCatalog(raw []byte, revision int64) (Catalog, error) {
	value := Catalog{Models: []string{}, HiddenModels: []string{}, Revision: revision}
	if len(raw) == 0 {
		return value, nil
	}
	if len(raw) > 128<<10 || json.Unmarshal(raw, &value) != nil || value.UpdatedAt <= 0 || value.Models == nil || len(value.Source) > 64 {
		return Catalog{}, ErrInput
	}
	if value.HiddenModels == nil {
		value.HiddenModels = []string{}
	}
	models, hidden, err := normalizedCatalogs(value.Models, value.HiddenModels)
	value.Models, value.HiddenModels = models, hidden
	return value, err
}

// Hidden entries share the visible list's budget and never duplicate a visible model.
func normalizedCatalogs(models, hidden []string) ([]string, []string, error) {
	if len(models)+len(hidden) > 512 {
		return nil, nil, ErrInput
	}
	models, err := normalizedCatalog(models)
	if err != nil {
		return nil, nil, err
	}
	hidden, err = normalizedCatalog(hidden)
	if err != nil {
		return nil, nil, err
	}
	result := make([]string, 0, len(hidden))
	for _, id := range hidden {
		if !slices.Contains(models, id) {
			result = append(result, id)
		}
	}
	return models, result, nil
}

// Contains reports whether the account can route the model, including hidden models.
func (c Catalog) Contains(model string) bool {
	return slices.Contains(c.Models, model) || slices.Contains(c.HiddenModels, model)
}

func normalizedCatalog(models []string) ([]string, error) {
	if len(models) > 512 {
		return nil, ErrInput
	}
	seen := make(map[string]bool, len(models))
	result := make([]string, 0, len(models))
	for _, id := range models {
		if !catalogModelID.MatchString(id) {
			return nil, ErrInput
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (s *Service) SaveCatalog(ctx context.Context, id string, revision int64, models, hidden []string, observedAt int64, source string) error {
	if _, err := s.get(ctx, id); err != nil {
		return err
	}
	if observedAt <= 0 || len(source) > 64 {
		return ErrInput
	}
	if hidden == nil {
		hidden = []string{}
	}
	models, hidden, err := normalizedCatalogs(models, hidden)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(Catalog{Models: models, HiddenModels: hidden, UpdatedAt: observedAt, Source: source})
	if err != nil {
		return err
	}
	// The conditional write prevents a late refresh from reviving pre-authorization or disabled data.
	changed, err := s.queries.SaveAccountCatalog(ctx, db.SaveAccountCatalogParams{ID: id, Revision: revision, Snapshot: raw, ObservedAt: observedAt})
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrCatalogChanged
	}
	return nil
}
