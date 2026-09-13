// Package httpx holds the small pieces every resource handler repeats:
// parsing the list query string the same way WeaponQuery/ManufacturerQuery/...
// encode it on the Flutter side, and wrapping results in the list envelope
// from КОНТРАКТ-API.md §1.1.
package httpx

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// Page mirrors the envelope every list endpoint returns. totalPages is sent
// even though the Dart PageResult recomputes it itself, since the contract
// requires it and other clients rely on it.
type Page[T any] struct {
	Items      []T `json:"items"`
	PageNum    int `json:"page"`
	Size       int `json:"size"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

func NewPage[T any](items []T, page, size, total int) Page[T] {
	if items == nil {
		items = []T{}
	}
	totalPages := 1
	if total > 0 {
		totalPages = (total + size - 1) / size
	}
	return Page[T]{Items: items, PageNum: page, Size: size, Total: total, TotalPages: totalPages}
}

func QueryInt(q url.Values, key string) (int, bool) {
	raw := q.Get(key)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

// IncludeDeleted mirrors WeaponQuery.toQueryParameters: the flag is only
// ever sent as "deleted=1", never "deleted=0" or "includeDeleted=...".
func IncludeDeleted(q url.Values) bool {
	return q.Get("deleted") == "1"
}

func ParsePage(q url.Values) int {
	if n, ok := QueryInt(q, "page"); ok && n >= 1 {
		return n
	}
	return 1
}

func ParseSize(q url.Values) int {
	n, ok := QueryInt(q, "size")
	if !ok || n < 1 {
		return 10
	}
	if n > 100 {
		return 100
	}
	return n
}

// ParseSort splits the client's "field,asc|desc" pair (WeaponQuery encodes
// it as one comma-joined parameter, not two) and resolves the field against
// an allowlist of real column names so a request can never smuggle
// arbitrary SQL into ORDER BY. Falls back to defaultField/ascending on an
// unknown or missing field.
func ParseSort(q url.Values, allowed map[string]string, defaultField string) (column string, ascending bool) {
	raw := q.Get("sort")
	field := defaultField
	ascending = true
	if raw != "" {
		parts := splitOnce(raw, ',')
		if parts[0] != "" {
			field = parts[0]
		}
		if len(parts) > 1 && parts[1] == "desc" {
			ascending = false
		}
	}
	column, ok := allowed[field]
	if !ok {
		column = allowed[defaultField]
	}
	return column, ascending
}

func splitOnce(s string, sep byte) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteOK(w http.ResponseWriter, v any) { WriteJSON(w, http.StatusOK, v) }

// PathID reads the {id} chi URL param and rejects anything that is not a
// positive integer with 400 rather than letting it fall through to a
// confusing 404 or a SQL type error.
func PathID(r *http.Request, raw string) (int, bool) {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
