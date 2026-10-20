package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/murongg/SubLane/internal/tenants"
)

func (h *authHTTP) registerMembers(router chi.Router) {
	routeErrors(router)
	router.Get("/", h.listMembers)
	router.Post("/", h.createMember)
	router.With(h.requireOrigin).Post("/invitations", h.createInvitation)
	router.Patch("/{id}", h.memberStatus)
	router.Patch("/{id}/role", h.memberRole)
	router.With(h.throttleLogin).Post("/{id}/password", h.resetMemberPassword)
}

func (h *authHTTP) createInvitation(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	invitation, err := h.service.CreateInvitation(r.Context(), h.tenantID, sessionUser(r).ID)
	if err != nil {
		if errors.Is(err, tenants.ErrForbidden) {
			writeJSON(w, 403, map[string]string{"error": "forbidden"})
		} else {
			authError(w, err)
		}
		return
	}
	writeJSON(w, 201, invitation)
}

func (h *authHTTP) memberRole(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeJSON(w, 400, map[string]string{"error": "invalid_input"})
		return
	}
	var input struct {
		Role tenants.Role `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if h.tenants == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	member, err := h.tenants.SetMemberRole(r.Context(), sessionUser(r).ID, h.tenantID, id, input.Role)
	if err != nil {
		switch {
		case errors.Is(err, tenants.ErrNotFound):
			writeJSON(w, 404, map[string]string{"error": "member_not_found"})
		case errors.Is(err, tenants.ErrForbidden):
			writeJSON(w, 403, map[string]string{"error": "forbidden"})
		case errors.Is(err, tenants.ErrInput):
			writeJSON(w, 400, map[string]string{"error": "invalid_input"})
		default:
			writeJSON(w, 503, map[string]string{"error": "unavailable"})
		}
		return
	}
	writeJSON(w, 200, member)
}

func (h *authHTTP) listMembers(w http.ResponseWriter, r *http.Request) {
	paging, valid := parsePagination(r.URL.Query())
	if !valid {
		writeJSON(w, 400, map[string]string{"error": "invalid_input"})
		return
	}
	if h.tenants == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	page, err := h.tenants.ListMembersPage(r.Context(), h.tenantID, paging)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (h *authHTTP) createMember(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if !decodeJSON(w, r, &input) {
		return
	}
	member, err := h.service.CreateMemberForTenant(r.Context(), h.tenantID, input.Username, input.Password)
	if err != nil {
		authError(w, err)
		return
	}
	writeJSON(w, 201, member)
}

func (h *authHTTP) memberStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeJSON(w, 400, map[string]string{"error": "invalid_input"})
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_input"})
		return
	}
	if h.tenants == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	member, err := h.tenants.SetMemberEnabled(r.Context(), sessionUser(r).ID, h.tenantID, id, *input.Enabled)
	if err != nil {
		switch {
		case errors.Is(err, tenants.ErrNotFound):
			writeJSON(w, 404, map[string]string{"error": "member_not_found"})
		case errors.Is(err, tenants.ErrForbidden):
			writeJSON(w, 403, map[string]string{"error": "forbidden"})
		default:
			writeJSON(w, 503, map[string]string{"error": "unavailable"})
		}
		return
	}
	writeJSON(w, 200, member)
}
