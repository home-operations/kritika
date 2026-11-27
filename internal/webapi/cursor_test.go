package webapi

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/tuning"
)

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 30, 0, 123, time.UTC)
	tests := []struct {
		name string
		c    store.Cursor
	}{
		{"time keyed", store.Cursor{T: at, ID: "0b7e7a8e-8f0c-4c6e-8a55-1f8f0c0f1a2b"}},
		{"text keyed", store.Cursor{S: "acme/widgets", ID: "id-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeCursor(encodeCursor(tt.c))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !got.T.Equal(tt.c.T) || got.S != tt.c.S || got.ID != tt.c.ID {
				t.Errorf("round trip = %+v, want %+v", got, tt.c)
			}
		})
	}
}

func TestParsePage(t *testing.T) {
	valid := encodeCursor(store.Cursor{S: "a", ID: "b"})
	tests := []struct {
		name      string
		query     string
		wantLimit int
		wantFirst bool
		wantCode  ErrorCode
	}{
		{"defaults", "", tuning.Get().WebAPI.DefaultLimit, true, ""},
		{"explicit limit", "limit=10", 10, true, ""},
		{"limit capped", "limit=5000", tuning.Get().WebAPI.MaxLimit, true, ""},
		{"zero limit", "limit=0", 0, false, CodeBadRequest},
		{"negative limit", "limit=-1", 0, false, CodeBadRequest},
		{"non-numeric limit", "limit=ten", 0, false, CodeBadRequest},
		{"cursor", "cursor=" + valid, tuning.Get().WebAPI.DefaultLimit, false, ""},
		{"garbage cursor", "cursor=@@", 0, false, CodeInvalidCursor},
		{"non-json cursor", "cursor=bm90IGpzb24", 0, false, CodeInvalidCursor},
		{"cursor without id", "cursor=" + encodeCursor(store.Cursor{S: "a"}), 0, false, CodeInvalidCursor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/x?"+tt.query, nil)
			p, err := parsePage(r)
			if tt.wantCode != "" {
				e, ok := errors.AsType[*apiError](err)
				if !ok || e.code != tt.wantCode || e.status != 400 {
					t.Fatalf("err = %v, want 400 %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePage: %v", err)
			}
			if p.Limit != tt.wantLimit || p.After.First() != tt.wantFirst {
				t.Errorf("page = %+v, want limit %d first %v", p, tt.wantLimit, tt.wantFirst)
			}
		})
	}
}

func TestNewPage(t *testing.T) {
	if got, err := jsonv2.Marshal(newPage([]int(nil), nil), jsonOptions); err != nil || string(got) != `{"items":[],"nextCursor":null}` {
		t.Errorf("empty page = %s, %v; want [] and null cursor", got, err)
	}
	next := store.Cursor{S: "a", ID: "b"}
	got := newPage([]int{1}, &next)
	if got.NextCursor == nil {
		t.Fatal("NextCursor = nil")
	}
	if c, err := decodeCursor(*got.NextCursor); err != nil || c.ID != "b" {
		t.Errorf("NextCursor decodes to %+v, %v", c, err)
	}
}

// TestPageSizesAreTunable: a list's page sizes, and the MCP tools'
// description of them, are tuning's WebAPI values as they are when read.
func TestPageSizesAreTunable(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) { v.WebAPI.DefaultLimit, v.WebAPI.MaxLimit = 7, 9 })
	for query, want := range map[string]int{"": 7, "limit=8": 8, "limit=5000": 9} {
		if p, err := parsePage(httptest.NewRequest("GET", "/x?"+query, nil)); err != nil || p.Limit != want {
			t.Errorf("parsePage(%q) = %d, %v; want %d", query, p.Limit, err, want)
		}
	}
	if p, err := (pageArgs{Limit: 5000}).page(); err != nil || p.Limit != 9 {
		t.Errorf("an MCP page of 5000 = %d, %v; want the cap of 9", p.Limit, err)
	}
	if got := pagedSchema[followupsArgs]().Properties["limit"].Description; got != "how many to return: 7 unless given, 9 at most" {
		t.Errorf("limit's description = %q", got)
	}
}

// TestTokenLifetimeChoices: an error names tuning's WebAPI.TokenLifetimes
// as they are.
func TestTokenLifetimeChoices(t *testing.T) {
	day := 24 * time.Hour
	for _, tt := range []struct {
		lifetimes []time.Duration
		want      string
	}{
		{[]time.Duration{0, 7 * day, 30 * day, 90 * day, 365 * day}, "7, 30, 90, 365, or 0 for never"},
		{[]time.Duration{14 * day}, "14"},
		{[]time.Duration{0}, "0 for never"},
	} {
		tuning.Override(t, func(v *tuning.Values) { v.WebAPI.TokenLifetimes = tt.lifetimes })
		if got := tokenLifetimeChoices(); got != tt.want {
			t.Errorf("choices of %v = %q, want %q", tt.lifetimes, got, tt.want)
		}
		if !tokenLifetime(int(tt.lifetimes[0]/day)) || tokenLifetime(1) {
			t.Errorf("lifetimes %v: the first offered is refused, or 1 day is taken", tt.lifetimes)
		}
	}
}

// TestTokenLifetimeOutOfRange: a request for days that, multiplied into a
// duration, would overflow into an offered lifetime is refused. Negative
// days would otherwise make a token that never expires.
func TestTokenLifetimeOutOfRange(t *testing.T) {
	day := 24 * time.Hour
	tuning.Override(t, func(v *tuning.Values) { v.WebAPI.TokenLifetimes = []time.Duration{0, 7 * day} })
	// A day is 2^16 times an odd number of nanoseconds, so 2^48 days are a
	// multiple of 2^64 nanoseconds, which wraps to 0.
	wraps := 1 << 48
	for _, days := range []int{-1, -7, wraps, -wraps, math.MaxInt, math.MinInt} {
		if tokenLifetime(days) {
			t.Errorf("tokenLifetime(%d) = true, want it refused", days)
		}
	}
	if !tokenLifetime(0) || !tokenLifetime(7) {
		t.Error("an offered lifetime is refused")
	}
}
