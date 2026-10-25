package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/model"
)

// AuthorizeRunner verifies the database capability before any runner work.
func (s *Store) AuthorizeRunner(ctx context.Context, runID string) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM runner_runs WHERE id = $1`, runID).Scan(&id); err != nil {
			return fmt.Errorf("store: authorize runner: %w", err)
		}
		return nil
	})
}

func (s *Store) HeartbeatRunner(ctx context.Context, runID string) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runner_runs SET heartbeat_at = now() WHERE id = $1`, runID); err != nil {
			return fmt.Errorf("store: runner heartbeat: %w", err)
		}
		return nil
	})
}

func (s *Store) SetRunnerPhase(ctx context.Context, runID, phase string) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error { return setRunnerPhase(ctx, tx, runID, phase) })
}

func setRunnerPhase(ctx context.Context, tx pgx.Tx, runID, phase string) error {
	tag, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = $2 WHERE id = $1`, runID, phase)
	if err != nil {
		return fmt.Errorf("store: set runner phase: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: run %s is not visible to this role", runID)
	}
	return nil
}

// RecordRunnerFailure accepts an already redacted error. Only the executor may
// write finished_at; a runner can report its phase and error alone.
func (s *Store) RecordRunnerFailure(ctx context.Context, runID, cause string) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = 'failed', error = left($2, 2000) WHERE id = $1`, runID, cause); err != nil {
			return fmt.Errorf("store: record runner failure: %w", err)
		}
		return nil
	})
}

// RunnerContextPack contains the runner's review decisions and encoded context.
// Account identity is always copied from its authorized run, never supplied.
type RunnerContextPack struct {
	Head, Base, PatchID, Diff                  string
	ChangedPaths                               []string
	Stages, Files                              json.RawMessage
	Notes                                      []string
	PriorHead                                  *string
	DeltaDiff                                  string
	DeltaPaths                                 []string
	Scope, ScopeReason, SkipReason, SkipDetail string
	RuleIDs                                    []string
}

// WriteRunnerContext commits the context and its next phase together.
func (s *Store) WriteRunnerContext(ctx context.Context, runID string, p RunnerContextPack, phase string) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, changed_paths, stages, repo_files, repo_notes,
				prior_head_sha, delta_diff, delta_paths, scope, scope_reason, skip_reason, rule_ids, skip_detail)
			SELECT id, account_id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17 FROM runner_runs WHERE id = $1`,
			runID, p.Head, p.Base, p.PatchID, p.Diff, p.ChangedPaths, p.Stages, p.Files, p.Notes,
			p.PriorHead, p.DeltaDiff, p.DeltaPaths, p.Scope, p.ScopeReason, p.SkipReason, p.RuleIDs, p.SkipDetail)
		if err != nil {
			return fmt.Errorf("store: write runner context: %w", err)
		}
		return setRunnerPhase(ctx, tx, runID, phase)
	})
}

// RunnerAgentRecord is a redacted agent result and optional retained conversation.
// JSON fields carry the runner's encoded payloads; nil Result is SQL NULL.
type RunnerAgentRecord struct {
	Stop, ForcedBy               string
	Result                       json.RawMessage
	Steps                        int
	ToolCalls, Timeline, Sources json.RawMessage
	Usage                        model.Usage
	CostUSD                      float64
	UnpricedSteps                int
	SkillsOffered, SkillsOpened  []string
	CommandsOffered, CommandsRun []string
	CommandErrors                []string
	CommandCounts                map[string]int
	Model, Error, ContinuedFrom  string
	Parts                        json.RawMessage
	Conversation                 []byte
	Tokens                       int64
	Session, GrantedModel        string
}

// WriteRunnerAgent commits the result, retained conversation, and phase together.
func (s *Store) WriteRunnerAgent(ctx context.Context, runID string, rec RunnerAgentRecord, phase string) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO agent_runs (runner_run_id, account_id, stop_reason, result, steps, tool_calls, timeline,
				input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_usd, model, error, sources,
				skills_offered, skills_opened, commands_offered, commands_run, continued_from, parts,
				unpriced_steps, command_errors, command_counts, forced_by)
			SELECT id, account_id, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10, $11, $12, left($13, 2000), $14, $15, $16, $17, $18,
				nullif($19, '')::uuid, coalesce($20::jsonb, '[]'::jsonb), $21, $22, coalesce($23::jsonb, '{}'::jsonb), $24
			FROM runner_runs WHERE id = $1`,
			runID, rec.Stop, rec.Result, rec.Steps, rec.ToolCalls, rec.Timeline,
			rec.Usage.Input, rec.Usage.CacheRead, rec.Usage.CacheWrite, rec.Usage.Output, rec.CostUSD, rec.Model, rec.Error,
			rec.Sources, rec.SkillsOffered, rec.SkillsOpened, rec.CommandsOffered, rec.CommandsRun, rec.ContinuedFrom, rec.Parts,
			rec.UnpricedSteps, rec.CommandErrors, rec.CommandCounts, rec.ForcedBy)
		if err != nil {
			return fmt.Errorf("store: write agent run: %w", err)
		}
		if rec.Conversation != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations (runner_run_id, account_id, session, conversation, tokens, model)
				SELECT id, account_id, $2, $3, $4, $5 FROM runner_runs WHERE id = $1`,
				runID, rec.Session, string(rec.Conversation), rec.Tokens, rec.GrantedModel); err != nil {
				return fmt.Errorf("store: write conversation: %w", err)
			}
		}
		return setRunnerPhase(ctx, tx, runID, phase)
	})
}

// WriteRunnerIndex commits all staged batches, the pack, and the done phase
// together. Account identity comes from the authorized runner row.
func (s *Store) WriteRunnerIndex(ctx context.Context, runID string, p IndexPack, chunks []StagedChunk) error {
	return s.WithRunnerJob(ctx, runID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO index_packs (runner_run_id, account_id, base_sha, mode, changed_paths, chunk_count)
			SELECT id, account_id, $2, $3, $4, $5 FROM runner_runs WHERE id = $1`,
			runID, p.Base, p.Mode, p.ChangedPaths, len(chunks)); err != nil {
			return fmt.Errorf("store: write runner index: %w", err)
		}
		for batch := range slices.Chunk(chunks, 500) {
			if err := stageRunnerChunks(ctx, tx, runID, batch); err != nil {
				return err
			}
		}
		return setRunnerPhase(ctx, tx, runID, "done")
	})
}

func stageRunnerChunks(ctx context.Context, tx pgx.Tx, runID string, chunks []StagedChunk) error {
	n := len(chunks)
	paths, langs, symbols := make([]string, n), make([]string, n), make([]string, n)
	kinds, scopes, texts := make([]string, n), make([]string, n), make([]string, n)
	starts, ends := make([]int32, n), make([]int32, n)
	for i, c := range chunks {
		paths[i], starts[i], ends[i] = c.Path, int32(c.StartLine), int32(c.EndLine)
		langs[i], symbols[i], kinds[i], scopes[i], texts[i] = c.Language, c.Symbol, c.Kind, c.Scope, c.Text
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO index_staging (runner_run_id, account_id, path, start_line, end_line, language, symbol, kind, scope, text)
		SELECT r.id, r.account_id, c.path, c.start_line, c.end_line, c.language, c.symbol, c.kind, c.scope, c.text
		FROM runner_runs r, unnest($2::text[], $3::int[], $4::int[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[])
			AS c (path, start_line, end_line, language, symbol, kind, scope, text)
		WHERE r.id = $1`, runID, paths, starts, ends, langs, symbols, kinds, scopes, texts)
	if err != nil {
		return fmt.Errorf("store: stage chunks: %w", err)
	}
	return nil
}
