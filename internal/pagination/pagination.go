// Package pagination bounds numbered pages while preserving legacy cursor queries.
package pagination

type Input struct {
	Page, Limit, Cursor int64
}

func (p Input) Valid() bool {
	return p.Page >= 0 && p.Limit >= 0 && p.Limit <= 100 && p.Cursor >= 0 && (p.Cursor == 0 || p.Page == 0)
}

type Metadata struct {
	Total    int64 `json:"total"`
	Page     int64 `json:"page"`
	PageSize int64 `json:"page_size"`
}

func (p Input) Resolve(total int64) Metadata {
	size := p.Limit
	if size == 0 {
		size = 50
	}
	// Clamp before calculating the offset, including oversized client page numbers.
	page := min(max(1, p.Page), max(1, 1+(total-1)/size))
	return Metadata{Total: total, Page: page, PageSize: size}
}

func (p Metadata) Offset() int64 { return (p.Page - 1) * p.PageSize }
