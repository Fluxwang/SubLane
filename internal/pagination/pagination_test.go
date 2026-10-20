package pagination

import (
	"math"
	"testing"
)

func TestBounds(t *testing.T) {
	for _, tc := range []struct {
		input                     Input
		total, page, size, offset int64
	}{
		{Input{}, 121, 1, 50, 0},
		{Input{Page: 3, Limit: 25}, 121, 3, 25, 50},
		{Input{Page: math.MaxInt64, Limit: 100}, 121, 2, 100, 100},
		{Input{Page: 4, Limit: 10}, 0, 1, 10, 0},
		{Input{Cursor: 99}, 121, 1, 50, 0},
	} {
		if !tc.input.Valid() {
			t.Fatalf("valid input rejected: %+v", tc.input)
		}
		got := tc.input.Resolve(tc.total)
		if got.Total != tc.total || got.Page != tc.page || got.PageSize != tc.size || got.Offset() != tc.offset {
			t.Fatalf("input=%+v total=%d result=%+v offset=%d", tc.input, tc.total, got, got.Offset())
		}
	}
	for _, input := range []Input{{Page: -1}, {Limit: -1}, {Limit: 101}, {Cursor: -1}, {Cursor: 1, Page: 1}} {
		if input.Valid() {
			t.Errorf("invalid input accepted: %+v", input)
		}
	}
}
