package webapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

// encodeCursor makes c the opaque token a client passes back as ?cursor=.
// It is only a position, not a capability: every read it feeds still runs
// under the requesting principal's account scope.
func encodeCursor(c store.Cursor) string {
	raw, _ := json.Marshal(c) // a struct of a time and two strings always marshals
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (store.Cursor, error) {
	var c store.Cursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, errBadRequest(CodeInvalidCursor, "cursor is not valid")
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return store.Cursor{}, errBadRequest(CodeInvalidCursor, "cursor is not valid")
	}
	return c, nil
}

// parsePage reads ?cursor= and ?limit=.
func parsePage(r *http.Request) (store.Page, error) {
	t := tuning.Get().WebAPI
	p := store.Page{Limit: t.DefaultLimit}
	q := r.URL.Query()
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return p, errBadRequest(CodeBadRequest, "limit must be a positive integer")
		}
		p.Limit = min(n, t.MaxLimit)
	}
	if s := q.Get("cursor"); s != "" {
		c, err := decodeCursor(s)
		if err != nil {
			return p, err
		}
		p.After = c
	}
	return p, nil
}

// newPage wraps a page of items, never null, and the next page's cursor.
func newPage[T any](items []T, next *store.Cursor) Page[T] {
	p := Page[T]{Items: items}
	if next != nil {
		s := encodeCursor(*next)
		p.NextCursor = &s
	}
	return p
}
