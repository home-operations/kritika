package poller

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/store"
)

// TestBranchUnknownLogsOncePerConnection: the same repository and branch
// missing on two connections is logged once for each, as the warning names
// the connection.
func TestBranchUnknownLogsOncePerConnection(t *testing.T) {
	var logs bytes.Buffer
	p := &Poller{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	r := store.PollRepo{Name: "onedr0p/home-ops", DefaultBranch: "main"}
	for _, in := range []*configfile.Connection{{Name: "first"}, {Name: "second"}, {Name: "first"}, {Name: "second"}} {
		p.branchUnknown(in, r, nil)
	}
	for _, name := range []string{"first", "second"} {
		if got := strings.Count(logs.String(), "connection="+name+" "); got != 1 {
			t.Errorf("connection %s logged %d times, want once:\n%s", name, got, logs.String())
		}
	}
}
