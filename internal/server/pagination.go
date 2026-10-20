package server

import (
	"net/url"
	"strconv"

	"github.com/murongg/SubLane/internal/pagination"
)

func parsePagination(values url.Values) (pagination.Input, bool) {
	var input pagination.Input
	for key, target := range map[string]*int64{"cursor": &input.Cursor, "page": &input.Page, "limit": &input.Limit} {
		if raw := values.Get(key); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return input, false
			}
			*target = value
		}
	}
	return input, input.Valid()
}
