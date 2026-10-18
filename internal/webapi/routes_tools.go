package webapi

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/store"
)

// ToolUsage ranks commands across the instance's currently served accounts.
type ToolUsage struct {
	From            time.Time       `json:"from"`
	To              time.Time       `json:"to"`
	RecordedRuns    int64           `json:"recordedRuns"`
	UncountedRuns   int64           `json:"uncountedRuns"`
	TotalExecutions int64           `json:"totalExecutions"`
	Tools           []ToolExecution `json:"tools"`
}

// ToolExecution counts starts of one command, across versions and providers.
type ToolExecution struct {
	Name       string `json:"name"`
	Executions int64  `json:"executions"`
}

func (s *Server) getToolUsage(w http.ResponseWriter, r *http.Request) error {
	from, to, err := s.window(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	out := ToolUsage{From: from, To: to}
	counts := map[string]int64{}
	for _, a := range readable(s.current.Get(), auth.PrincipalFrom(ctx)) {
		var data store.CommandUsage
		if err := s.store.WithAccount(ctx, a.ID(), func(tx pgx.Tx) error {
			var err error
			data, err = store.ReadCommandUsage(ctx, tx, from, to)
			return err
		}); err != nil {
			return err
		}
		out.RecordedRuns += data.RecordedRuns
		out.UncountedRuns += data.UncountedRuns
		for name, count := range data.Counts {
			counts[name] += count
			out.TotalExecutions += count
		}
	}
	out.Tools = topTools(counts, 10)
	writeJSON(w, http.StatusOK, out)
	return nil
}

func topTools(counts map[string]int64, limit int) []ToolExecution {
	rows := make([]ToolExecution, 0, len(counts))
	for name, count := range counts {
		rows = append(rows, ToolExecution{Name: name, Executions: count})
	}
	slices.SortFunc(rows, func(a, b ToolExecution) int {
		return cmp.Or(cmp.Compare(b.Executions, a.Executions), cmp.Compare(a.Name, b.Name))
	})
	return rows[:min(len(rows), limit)]
}
