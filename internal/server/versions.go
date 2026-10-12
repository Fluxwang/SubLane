package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/murongg/SubLane/internal/versions"
)

type versionHTTP struct {
	service  *versions.Service
	provider versions.Provider
}

func (h *versionHTTP) register(router chi.Router) {
	routeErrors(router)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.service == nil {
				writeJSON(w, 503, map[string]string{"error": "unavailable"})
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	router.Get("/", h.get)
	router.Patch("/", h.save)
	router.Post("/sync", h.sync)
}
func (h *versionHTTP) get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, h.service.View())
}
func (h *versionHTTP) save(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ManualVersion *string `json:"manual_version"`
		AutoSync      *bool   `json:"auto_sync"`
	}
	if !decodeJSONLimit(w, r, &input, 1024) {
		return
	}
	if input.ManualVersion == nil || input.AutoSync == nil {
		h.versionError(w, versions.ErrInput)
		return
	}
	value, err := h.service.Save(r.Context(), versions.Config{ManualVersion: *input.ManualVersion, AutoSync: *input.AutoSync})
	if err != nil {
		h.versionError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (h *versionHTTP) sync(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	value, err := h.service.Sync(r.Context(), true)
	if err != nil {
		if errors.Is(err, versions.ErrCooldown) {
			w.Header().Set("Retry-After", strconv.FormatInt(max(1, value.RetryAfterSeconds), 10))
		}
		h.versionError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
func (h *versionHTTP) versionError(w http.ResponseWriter, err error) {
	provider := string(h.provider)
	status, code := 503, provider+"_version_sync_failed"
	if errors.Is(err, versions.ErrInput) {
		status, code = 400, "invalid_"+provider+"_version"
	}
	if errors.Is(err, versions.ErrCooldown) {
		status, code = 429, provider+"_version_sync_cooldown"
	}
	if errors.Is(err, versions.ErrStorage) {
		code = provider + "_version_storage_failed"
	}
	writeJSON(w, status, map[string]string{"error": code})
}
