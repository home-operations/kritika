package webapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/store"
)

// indexChunkLimit bounds the chunks one file's listing returns.
const indexChunkLimit = 200

// indexQueue is where each of the account's repositories stands for its
// next index: its index jobs queued or running, running ones first, or
// its place in the onboarding backlog.
type indexQueue struct {
	jobs    map[string][]IndexJob
	backlog map[string]int
}

// readIndexQueue reads the account's index queue. The backlog is the
// onboarding feeder's order for the account, less the repositories its
// settings leave off, as the feeder passes them over; without an embedder
// the feeder offers nothing, so nothing waits.
func readIndexQueue(ctx context.Context, tx pgx.Tx, t *accountScope) (indexQueue, error) {
	q := indexQueue{jobs: map[string][]IndexJob{}, backlog: map[string]int{}}
	live, err := store.LiveIndexJobs(ctx, tx, "")
	if err != nil {
		return q, err
	}
	for _, j := range live {
		q.jobs[j.RepositoryID] = append(q.jobs[j.RepositoryID], indexJob(j))
	}
	if t.file.Embedding == nil {
		return q, nil
	}
	waiting, err := store.OnboardBacklog(ctx, tx, jobs.OnboardRetryAfter)
	if err != nil {
		return q, err
	}
	for _, r := range waiting {
		if t.file.Runs(t.account, r.RepoTraits) {
			q.backlog[r.ID] = len(q.backlog) + 1
		}
	}
	return q, nil
}

// apply records on r where it stands.
func (q indexQueue) apply(r *Repository) {
	if js := q.jobs[r.ID]; len(js) > 0 {
		r.Index.Job = &js[0]
	}
	r.Index.Backlog = q.backlog[r.ID]
}

func indexJob(j store.IndexJobRow) IndexJob {
	return IndexJob{
		ID: j.ID, State: j.State, Trigger: j.Trigger, Full: j.Full, Attempt: j.Attempt,
		CreatedAt: j.CreatedAt, AttemptedAt: j.AttemptedAt,
	}
}

// getIndex serves what the repository's active index generation holds.
func (s *Server) getIndex(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	ctx := r.Context()
	var c store.IndexContents
	err := s.read(ctx, t, func(tx pgx.Tx) error {
		repo, err := findRepo(ctx, tx, r)
		if err != nil {
			return err
		}
		c, err = store.ReadIndexContents(ctx, tx, repo.ID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, IndexContents{})
		return nil
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, IndexContents{
		Indexed: true, Commit: c.Commit, EmbedModel: c.EmbedModel, EmbedDims: c.EmbedDims,
		BuiltAt: &c.CreatedAt, FinishedAt: c.FinishedAt, Chunks: c.Chunks, Files: c.Files, Current: c.Current,
	})
	return nil
}

// listIndexFiles serves a page of the files the repository's active
// index generation holds, those whose path holds q when it is set.
func (s *Server) listIndexFiles(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	page, err := parsePage(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var rows []store.IndexFileRow
	var next *store.Cursor
	err = s.read(ctx, t, func(tx pgx.Tx) error {
		runID, err := activeRun(ctx, tx, r)
		if err != nil || runID == "" {
			return err
		}
		rows, next, err = store.ListIndexFiles(ctx, tx, runID, r.URL.Query().Get("q"), page)
		return err
	})
	if err != nil {
		return err
	}
	items := make([]IndexFile, len(rows))
	for i, f := range rows {
		items[i] = IndexFile{Path: f.Path, Language: f.Language, Chunks: f.Chunks}
	}
	writeJSON(w, http.StatusOK, newPage(items, next))
	return nil
}

// listIndexChunks serves the chunks the repository's active index
// generation holds of one file, their text to an admin alone.
func (s *Server) listIndexChunks(w http.ResponseWriter, r *http.Request, t *accountScope) error {
	path := r.URL.Query().Get("path")
	if path == "" {
		return errBadRequest(CodeBadRequest, "path is required")
	}
	ctx := r.Context()
	var rows []store.IndexChunkRow
	err := s.read(ctx, t, func(tx pgx.Tx) error {
		runID, err := activeRun(ctx, tx, r)
		if err != nil || runID == "" {
			return err
		}
		rows, err = store.ListIndexChunks(ctx, tx, runID, path, indexChunkLimit+1)
		return err
	})
	if err != nil {
		return err
	}
	out := IndexChunks{Path: path, Items: []IndexChunk{}, Truncated: len(rows) > indexChunkLimit}
	for _, c := range rows[:min(len(rows), indexChunkLimit)] {
		chunk := IndexChunk{StartLine: c.StartLine, EndLine: c.EndLine, Language: c.Language, Symbol: c.Symbol, Kind: c.Kind, Scope: c.Scope}
		if t.principal.Admin {
			chunk.Text = c.Text
		}
		out.Items = append(out.Items, chunk)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// activeRun resolves {owner}/{repo} and returns its active index
// generation's ID, "" when it has none.
func activeRun(ctx context.Context, tx pgx.Tx, r *http.Request) (string, error) {
	repo, err := findRepo(ctx, tx, r)
	if err != nil {
		return "", err
	}
	return store.ActiveIndexRun(ctx, tx, repo.ID)
}
