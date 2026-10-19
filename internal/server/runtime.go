package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/gateway"
)

func (h *accountHTTP) runtime(w http.ResponseWriter, r *http.Request) {
	if h.gateway == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	values, err := h.gateway.Runtime(r.Context())
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"accounts": values, "server_time": time.Now().Unix()})
}
func (h *accountHTTP) limits(w http.ResponseWriter, r *http.Request) {
	var input struct {
		MaxConcurrency int64 `json:"max_concurrency"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if h.gateway == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.gateway.SetConcurrency(r.Context(), id, input.MaxConcurrency); err != nil {
		accountError(w, err)
		return
	}
	account, err := h.service.Get(r.Context(), id)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, 200, account)
}
func (h *accountHTTP) resume(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !decodeJSON(w, r, &input) {
		return
	}
	if h.gateway == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	if err := h.gateway.Resume(r.Context(), chi.URLParam(r, "id")); err != nil {
		accountError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *accountHTTP) requests(w http.ResponseWriter, r *http.Request) {
	h.requestHistory(w, r, false)
}

func (h *accountHTTP) personalRequests(w http.ResponseWriter, r *http.Request) {
	h.requestHistory(w, r, true)
}

func (h *accountHTTP) requestHistory(w http.ResponseWriter, r *http.Request, personal bool) {
	if h.gateway == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	values := r.URL.Query()
	filter := gateway.RequestFilter{AccountID: values.Get("account_id"), Outcome: values.Get("outcome"), Model: values.Get("model"), RequestID: values.Get("request_id")}
	numeric := map[string]*int64{"cursor": &filter.Cursor, "page": &filter.Page, "limit": &filter.Limit, "from": &filter.From, "until": &filter.Until, "key_id": &filter.KeyID}
	if !personal {
		numeric["user_id"] = &filter.MemberID
	}
	for key, target := range numeric {
		if value := values.Get(key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				accountError(w, accounts.ErrInput)
				return
			}
			*target = n
		}
	}
	var page gateway.RequestPage
	var err error
	if personal {
		page, err = h.gateway.UserRequests(r.Context(), sessionUser(r).ID, filter)
	} else {
		page, err = h.gateway.Requests(r.Context(), filter)
	}
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, 200, page)
}

func (h *accountHTTP) requestFilters(w http.ResponseWriter, r *http.Request) {
	h.requestCallers(w, r, 0)
}
func (h *accountHTTP) personalRequestFilters(w http.ResponseWriter, r *http.Request) {
	h.requestCallers(w, r, sessionUser(r).ID)
}
func (h *accountHTTP) requestCallers(w http.ResponseWriter, r *http.Request, userID int64) {
	if h.gateway == nil {
		writeJSON(w, 503, map[string]string{"error": "unavailable"})
		return
	}
	callers, err := h.gateway.RequestCallers(r.Context(), userID)
	if err != nil {
		accountError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"callers": callers})
}
