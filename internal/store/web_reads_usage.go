package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/jobtimeout"
)

// UsageGroup is what a usage series is keyed by.
type UsageGroup string

// Usage series groupings.
const (
	UsageByHour   UsageGroup = "hour"
	UsageByDay    UsageGroup = "day"
	UsageByWeek   UsageGroup = "week"
	UsageByModel  UsageGroup = "model"
	UsageByRepo   UsageGroup = "repo"
	UsageByRole   UsageGroup = "role"
	UsageByReview UsageGroup = "review"
)

// Valid reports whether g is a usage grouping.
func (g UsageGroup) Valid() bool {
	_, ok := usageKeys[g]
	return ok
}

// usageKeys are the SQL key expressions of each grouping: over usage u
// joined to its repository ur, and over model_calls m joined through its
// review to the repository mr. A model call's kind maps to the usage role
// it is charged as, and a follow-up's agent step to the follow-up's.
var usageKeys = map[UsageGroup][2]string{
	UsageByHour: {
		`to_char(date_trunc('hour', u.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`,
		`to_char(date_trunc('hour', m.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`,
	},
	UsageByDay: {`to_char(u.created_at, 'YYYY-MM-DD')`, `to_char(m.created_at, 'YYYY-MM-DD')`},
	UsageByWeek: {
		`to_char(date_trunc('week', u.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`,
		`to_char(date_trunc('week', m.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`,
	},
	UsageByReview: {
		`coalesce(u.review_id::text, '')`,
		`CASE WHEN m.followup_comment_id IS NOT NULL THEN '' ELSE coalesce(m.review_id::text, '') END`,
	},
	UsageByModel: {`u.model`, `m.model`},
	UsageByRepo:  {`coalesce(ur.name, '')`, `coalesce(mr.name, '')`},
	UsageByRole: {`u.role`, `CASE WHEN m.followup_comment_id IS NOT NULL THEN 'followup' WHEN m.kind = 'agent_step' THEN 'review'
		ELSE m.kind END`},
}

// UsageSeriesRow is one key of a usage series. Tokens, cost and calls come
// from the usage rows, the durable accounting. The cache token counts come
// from the model calls, which only the transcript retention keeps. So they
// cover at most that window.
type UsageSeriesRow struct {
	Key              string
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	OutputTokens     int64
	CostUSD          float64
	Calls            int64
	PlanCalls        int64
	UnpricedCalls    int64
}

// UsageFilter narrows both durable accounting and retained cache detail.
// A nil field leaves the dimension unfiltered. An empty value selects
// accounting rows with no attribution for that dimension.
type UsageFilter struct {
	Repository *string
	Model      *string
	// OrderByTokens ranks the bounded review groups by tokens instead of spend.
	OrderByTokens bool
}

// UsageSeries sums the account's usage in [from, to) by group, ordered by
// key. Review groups retain the top 50, unattributed usage, and a remainder
// bucket keyed __other__ so totals still cover the entire selection.
func UsageSeries(
	ctx context.Context, tx pgx.Tx, group UsageGroup, from, to time.Time, chatgptOnly bool, filter UsageFilter,
) ([]UsageSeriesRow, error) {
	keys, ok := usageKeys[group]
	if !ok {
		return nil, ErrFilter
	}
	query := `WITH billed AS (
			SELECT ` + keys[0] + ` AS key, sum(u.input_tokens) AS input, sum(u.output_tokens) AS output,
				sum(u.cost_usd)::float8 AS cost, count(*) AS calls, count(*) FILTER (WHERE u.chatgpt_plan) AS plan_calls,
				count(*) FILTER (WHERE u.unpriced) AS unpriced_calls
			FROM usage u LEFT JOIN repositories ur ON ur.id = u.repository_id
			WHERE u.created_at >= $1 AND u.created_at < $2 AND (NOT $3::boolean OR u.chatgpt_plan)
				AND ($4::text IS NULL OR coalesce(ur.name, '') = $4) AND ($5::text IS NULL OR u.model = $5) GROUP BY 1),
		cached AS (
			SELECT ` + keys[1] + ` AS key, sum(m.cache_read_tokens) AS cache_read, sum(m.cache_write_tokens) AS cache_write
			FROM model_calls m LEFT JOIN reviews mv ON mv.id = m.review_id
				LEFT JOIN pull_requests mp ON mp.id = mv.pull_request_id LEFT JOIN repositories mr ON mr.id = mp.repository_id
			WHERE m.created_at >= $1 AND m.created_at < $2 AND (NOT $3::boolean OR m.chatgpt_plan)
				AND ($4::text IS NULL OR coalesce(mr.name, '') = $4) AND ($5::text IS NULL OR m.model = $5) GROUP BY 1)
		SELECT coalesce(b.key, c.key) AS key, coalesce(b.input, 0) AS input,
			coalesce(c.cache_read, 0) AS cache_read, coalesce(c.cache_write, 0) AS cache_write,
			coalesce(b.output, 0) AS output, coalesce(b.cost, 0) AS cost, coalesce(b.calls, 0) AS calls,
			coalesce(b.plan_calls, 0) AS plan_calls, coalesce(b.unpriced_calls, 0) AS unpriced_calls
		FROM billed b FULL JOIN cached c ON c.key = b.key`
	if group == UsageByReview {
		metric := "cost"
		if filter.OrderByTokens {
			metric = "input + output"
		}
		// UUID keys cannot collide with the remainder bucket. Aggregating it
		// preserves selection totals while bounding the response and chart.
		query = `WITH series AS (` + query + `), ranked AS (
			SELECT *, row_number() OVER (ORDER BY (key = ''), ` + metric + ` DESC, key) AS rank FROM series)
			SELECT CASE WHEN key = '' OR rank <= 50 THEN key ELSE '__other__' END AS key,
				sum(input)::bigint, sum(cache_read)::bigint, sum(cache_write)::bigint, sum(output)::bigint,
				sum(cost)::float8, sum(calls)::bigint, sum(plan_calls)::bigint, sum(unpriced_calls)::bigint
			FROM ranked GROUP BY 1`
	}
	rows, err := tx.Query(ctx, query+` ORDER BY 1`, from, to, chatgptOnly, filter.Repository, filter.Model)
	if err != nil {
		return nil, fmt.Errorf("store: usage series: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (UsageSeriesRow, error) {
		var u UsageSeriesRow
		err := row.Scan(&u.Key, &u.InputTokens, &u.CacheReadTokens, &u.CacheWriteTokens, &u.OutputTokens, &u.CostUSD,
			&u.Calls, &u.PlanCalls, &u.UnpricedCalls)
		return u, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: usage series: %w", err)
	}
	return out, nil
}

// UsageComparison reads durable accounting only: comparisons do not show
// retained cache detail. Time buckets and review IDs compare as one total.
func UsageComparison(
	ctx context.Context, tx pgx.Tx, group UsageGroup, from, to time.Time, chatgptOnly bool, filter UsageFilter,
) ([]UsageSeriesRow, error) {
	keys, ok := usageKeys[group]
	if !ok {
		return nil, ErrFilter
	}
	key := keys[0]
	switch group {
	case UsageByHour, UsageByDay, UsageByWeek, UsageByReview:
		key = `'Total'::text`
	}
	rows, err := tx.Query(ctx, `SELECT `+key+` AS key, sum(u.input_tokens)::bigint, sum(u.output_tokens)::bigint,
		sum(u.cost_usd)::float8, count(*), count(*) FILTER (WHERE u.chatgpt_plan), count(*) FILTER (WHERE u.unpriced)
		FROM usage u LEFT JOIN repositories ur ON ur.id = u.repository_id
		WHERE u.created_at >= $1 AND u.created_at < $2 AND (NOT $3::boolean OR u.chatgpt_plan)
			AND ($4::text IS NULL OR coalesce(ur.name, '') = $4) AND ($5::text IS NULL OR u.model = $5)
		GROUP BY 1 ORDER BY 1`, from, to, chatgptOnly, filter.Repository, filter.Model)
	if err != nil {
		return nil, fmt.Errorf("store: usage comparison: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (UsageSeriesRow, error) {
		var u UsageSeriesRow
		err := row.Scan(&u.Key, &u.InputTokens, &u.OutputTokens, &u.CostUSD, &u.Calls, &u.PlanCalls, &u.UnpricedCalls)
		return u, err
	})
	if err != nil {
		return nil, fmt.Errorf("store: usage comparison: %w", err)
	}
	return out, nil
}

// JobRow is one River job of the account, with the parts of its arguments
// the queue view shows.
type JobRow struct {
	ID                 int64
	Kind               string
	State              rivertype.JobState
	Attempt            int
	MaxAttempts        int
	CreatedAt          time.Time
	ScheduledAt        time.Time
	AttemptedAt        *time.Time
	FinalizedAt        *time.Time
	RepositoryID       string
	Repository         string
	Number             int
	Head               string
	Trigger            string
	CommentID          int64
	Resolved           *bool
	LastError          string
	HeartbeatAt        *time.Time
	HeartbeatOverdue   bool
	RecentFailure      bool
	CancelableReviewID string
}

// queueFinished bounds how many finished jobs the queue view lists.
// queueActive bounds the unfinished ones, which a healthy queue keeps far
// below it.
const (
	queueFinished = 50
	queueActive   = 500
)

// jobColumns selects a JobRow from the account's review, follow-up, thread and
// index jobs. River's tables carry no row-level security, so the account
// comes from the transaction's own setting rather than a parameter that a
// caller can get wrong.
var jobColumns = `j.id, j.kind, j.state::text, j.attempt, j.max_attempts, j.created_at, j.scheduled_at, j.attempted_at, j.finalized_at,
	coalesce(j.args->>'repository_id', ''), coalesce(r.name, ''), coalesce((j.args->>'number')::int, 0),
	coalesce(j.args->>'head_sha', j.args->>'commit_sha', ''), coalesce(j.args->>'trigger', ''),
	coalesce((j.args->>'comment_id')::bigint, 0), CASE WHEN j.kind = 'thread' THEN (j.args->>'resolved')::boolean END,
	coalesce(j.errors[array_length(j.errors, 1)]->>'error', ''),
	coalesce((SELECT v.id::text FROM reviews v JOIN pull_requests p ON p.id = v.pull_request_id
		WHERE p.repository_id = r.id AND p.number = (j.args->>'number')::int AND v.river_job_id = j.id
		AND j.state IN (` + jobs.LiveStatesSQL() + `)
		AND v.status IN ('running', 'prepared') AND v.cancel_requested_at IS NULL ORDER BY v.created_at DESC LIMIT 1), ''),
	h.heartbeat_at,
	coalesce(j.state = 'running' AND j.attempted_at IS NOT NULL
		AND greatest(j.attempted_at, h.heartbeat_at) < now() -
			interval '` + fmt.Sprint(jobtimeout.HeartbeatAbandonedAfter.Seconds()) + ` seconds', false),
	coalesce(j.state = 'discarded' AND j.finalized_at >= now() - interval '24 hours' AND NOT EXISTS (
		SELECT 1 FROM river_job newer WHERE newer.args->>'account_id' = current_setting('app.account_id', true)
			AND newer.id > j.id AND newer.kind = j.kind
			AND newer.args->>'repository_id' = j.args->>'repository_id'
			AND coalesce(newer.args->>'number', '') = coalesce(j.args->>'number', '')
			AND coalesce(newer.args->>'comment_id', '') = coalesce(j.args->>'comment_id', '')
			AND newer.state IN (` + jobs.LiveStatesSQL() + `, 'completed')), false) AS recent_failure
	FROM river_job j LEFT JOIN repositories r ON r.id::text = j.args->>'repository_id'
	LEFT JOIN job_heartbeats h ON h.job_id = j.id
	WHERE j.args->>'account_id' = current_setting('app.account_id', true)
	AND j.kind IN ('review', 'review_prepare', 'review_publish', 'followup', 'thread', 'index')`

func scanJob(row pgx.CollectableRow) (JobRow, error) {
	var j JobRow
	var state string
	err := row.Scan(&j.ID, &j.Kind, &state, &j.Attempt, &j.MaxAttempts, &j.CreatedAt, &j.ScheduledAt, &j.AttemptedAt, &j.FinalizedAt,
		&j.RepositoryID, &j.Repository, &j.Number, &j.Head, &j.Trigger, &j.CommentID, &j.Resolved, &j.LastError,
		&j.CancelableReviewID, &j.HeartbeatAt, &j.HeartbeatOverdue, &j.RecentFailure)
	j.State = rivertype.JobState(state)
	return j, err
}

// ListQueue returns the account's review, follow-up, thread and index jobs that
// did not finish, oldest first. It then returns finished ones, and recent
// failures take priority over the remaining history.
func ListQueue(ctx context.Context, tx pgx.Tx) ([]JobRow, error) {
	// Rank bounded history and failure candidates together so prioritizing
	// attention does not sort the account's entire completed-job history.
	rows, err := tx.Query(ctx, `(SELECT `+jobColumns+` AND j.state IN (`+jobs.LiveStatesSQL()+`)
			ORDER BY j.scheduled_at, j.id LIMIT $1)
		UNION ALL
		(SELECT * FROM (
			(SELECT * FROM (SELECT `+jobColumns+` AND j.state = 'discarded'
				AND j.finalized_at >= now() - interval '24 hours') failures
				WHERE recent_failure ORDER BY finalized_at DESC, id DESC LIMIT $2)
			UNION
			(SELECT `+jobColumns+` AND j.state IN ('completed', 'cancelled', 'discarded')
				ORDER BY j.finalized_at DESC NULLS LAST, j.id DESC LIMIT $2)
		) finished ORDER BY recent_failure DESC, finalized_at DESC NULLS LAST, id DESC LIMIT $2)`, queueActive, queueFinished)
	if err != nil {
		return nil, fmt.Errorf("store: list queue: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanJob)
	if err != nil {
		return nil, fmt.Errorf("store: list queue: %w", err)
	}
	return out, nil
}

// ReadHeldSlots counts, for each model the account has ever leased a slot
// of, the slots a live lease holds now.
func ReadHeldSlots(ctx context.Context, tx pgx.Tx) (map[string]int, error) {
	rows, err := tx.Query(ctx, `SELECT model_key, count(*) FILTER (WHERE job_id IS NOT NULL AND expires_at >= now())
		FROM model_leases GROUP BY model_key`)
	if err != nil {
		return nil, fmt.Errorf("store: read held slots: %w", err)
	}
	out := map[string]int{}
	var key string
	var held int
	if _, err := pgx.ForEachRow(rows, []any{&key, &held}, func() error {
		out[key] = held
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: read held slots: %w", err)
	}
	return out, nil
}

// FindLiveReviewJob returns the review job of a pull request that has not
// finished, the newest when there are several, or nil when there is none.
func FindLiveReviewJob(ctx context.Context, tx pgx.Tx, repositoryID string, number int) (*JobRow, error) {
	rows, err := tx.Query(ctx, `SELECT `+jobColumns+` AND j.kind IN ('review', 'review_prepare', 'review_publish')
		AND j.state IN (`+jobs.LiveStatesSQL()+`)
			AND j.args->>'repository_id' = $1 AND (j.args->>'number')::int = $2
		ORDER BY j.id DESC LIMIT 1`, repositoryID, number)
	if err != nil {
		return nil, fmt.Errorf("store: find live review job: %w", err)
	}
	j, err := pgx.CollectExactlyOneRow(rows, scanJob)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: find live review job: %w", err)
	}
	return &j, nil
}
