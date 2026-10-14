package webapi

import (
	"fmt"
	"testing"
)

// A host refused a little in many repositories outranks one refused much
// in one, and every row of a kept host stays.
func TestTopRefusedHosts(t *testing.T) {
	rows := []EgressRefusal{
		{Repository: "a/big", Host: "one.example", Count: 9},
		{Repository: "a/x", Host: "spread.example", Count: 4},
		{Repository: "a/y", Host: "spread.example", Count: 4},
		{Repository: "a/z", Host: "spread.example", Count: 4},
		{Repository: "a/x", Host: "few.example", Count: 2},
	}
	got := topRefusedHosts(rows, 2)
	want := "[{a/x spread.example 4} {a/y spread.example 4} {a/z spread.example 4} {a/big one.example 9}]"
	s := make([]string, 0, len(got))
	for _, r := range got {
		s = append(s, fmt.Sprintf("{%s %s %d}", r.Repository, r.Host, r.Count))
	}
	if fmt.Sprint(s) != want {
		t.Fatalf("got %v, want %s", s, want)
	}
	if n := len(topRefusedHosts(rows, 10)); n != len(rows) {
		t.Fatalf("under the limit, %d rows kept of %d", n, len(rows))
	}
}
