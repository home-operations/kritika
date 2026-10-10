package webapi

import (
	"cmp"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/auth"
	"github.com/home-operations/kritika/internal/store"
)

// defaultUsageWindow is the span a usage series covers without ?from=, and
// maxUsageWindow the most ?from= and ?to= may span: the series are read
// from every row in the span, and the analytics page reads the span
// before it too, so a span of years would scan the account's history
// twice on one request.
const (
	defaultUsageWindow = 30 * 24 * time.Hour
	maxUsageWindow     = 366 * 24 * time.Hour
)

// parseTime reads an RFC 3339 time or a YYYY-MM-DD date (midnight UTC).
func parseTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	t, err := time.Parse(time.DateOnly, s)
	return t, err == nil
}

// usageQuery reads ?group=, ?from= and ?to=: by day over the last 30 days
// unless given.
func (s *Server) usageQuery(r *http.Request) (store.UsageGroup, time.Time, time.Time, error) {
	group := cmp.Or(store.UsageGroup(r.URL.Query().Get("group")), store.UsageByDay)
	if !group.Valid() {
		return "", time.Time{}, time.Time{}, errBadRequest(CodeBadRequest, "group must be hour, day, week, model, repo or role")
	}
	from, to, err := s.window(r)
	return group, from, to, err
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	billing := r.URL.Query().Get("billing")
	if billing != "" && billing != "all" && billing != "chatgpt" {
		return errBadRequest(CodeBadRequest, "billing must be all or chatgpt")
	}
	group, from, to, err := s.usageQuery(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var rows []store.UsageSeriesRow
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		rows, err = store.UsageSeries(ctx, tx, group, from, to, billing == "chatgpt")
		return err
	}); err != nil {
		return err
	}
	out := UsageSeries{Group: group, From: from, To: to, Rows: make([]UsagePoint, len(rows))}
	for i, u := range rows {
		out.Rows[i] = UsagePoint{
			Key: u.Key, InputTokens: u.InputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens,
			OutputTokens: u.OutputTokens, CostUSD: u.CostUSD, Calls: u.Calls, PlanCalls: u.PlanCalls,
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) listQueue(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	ctx := r.Context()
	var rows []store.JobRow
	if err := s.read(ctx, t, func(tx pgx.Tx) error {
		var err error
		rows, err = store.ListQueue(ctx, tx)
		return err
	}); err != nil {
		return err
	}
	out := make([]Job, len(rows))
	for i, j := range rows {
		out[i] = jobItem(j)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// listInstanceQueue merges the queues of the accounts the viewer can read,
// each read under its own account's row-level security.
func (s *Server) listInstanceQueue(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	file := s.current.Get()
	out := InstanceQueue{Jobs: []InstanceJob{}, Slots: []ModelSlots{}}
	for _, t := range readable(file, auth.PrincipalFrom(ctx)) {
		var rows []store.JobRow
		var held map[string]int
		if err := s.store.WithAccount(ctx, t.ID(), func(tx pgx.Tx) error {
			var err error
			if rows, err = store.ListQueue(ctx, tx); err != nil {
				return err
			}
			held, err = store.ReadHeldSlots(ctx, tx)
			return err
		}); err != nil {
			return err
		}
		for _, j := range rows {
			out.Jobs = append(out.Jobs, InstanceJob{Job: jobItem(j), Account: t.Slug()})
		}
		// The account's own review model always shows, busy or not; another
		// model, a repository's choice, only while it holds a slot.
		settings := file.Settings(t, "")
		own := string(settings.Models.Review)
		if _, ok := held[own]; !ok && own != "" {
			held[own] = 0
		}
		for _, model := range slices.Sorted(maps.Keys(held)) {
			if held[model] > 0 || model == own {
				out.Slots = append(out.Slots, ModelSlots{Account: t.Slug(), Model: model, Held: held[model], Slots: settings.Limits.Concurrency})
			}
		}
	}
	// Unfinished jobs lead, the longest scheduled first, then the finished,
	// the latest first: each account's own order, kept across them.
	slices.SortStableFunc(out.Jobs, func(a, b InstanceJob) int {
		switch {
		case a.FinalizedAt == nil && b.FinalizedAt == nil:
			return cmp.Or(a.ScheduledAt.Compare(b.ScheduledAt), cmp.Compare(a.ID, b.ID))
		case a.FinalizedAt == nil:
			return -1
		case b.FinalizedAt == nil:
			return 1
		}
		return cmp.Or(b.FinalizedAt.Compare(*a.FinalizedAt), cmp.Compare(b.ID, a.ID))
	})
	writeJSON(w, http.StatusOK, out)
	return nil
}

func jobItem(j store.JobRow) Job {
	return Job{
		ID: j.ID, Kind: j.Kind, State: j.State, Attempt: j.Attempt, MaxAttempts: j.MaxAttempts, CreatedAt: j.CreatedAt,
		ScheduledAt: j.ScheduledAt, AttemptedAt: j.AttemptedAt, FinalizedAt: j.FinalizedAt, LastError: j.LastError, Cause: jobCause(j.LastError),
		Args: JobArgs{Repository: j.Repository, Number: j.Number, Head: j.Head, Trigger: j.Trigger, CommentID: j.CommentID},
	}
}
