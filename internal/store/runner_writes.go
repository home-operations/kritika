package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/runspec"
)

// A runner's writes are safe to repeat. A runner sends one again when the
// answer to it is lost, so a row it already wrote is left as it is.

// ErrIndexStaging is an index upload that another upload of the run overtook:
// sending it again makes that upload the current one.
var ErrIndexStaging = errors.New("store: the run's index staging changed during the upload")

// HeartbeatRunner stamps the run's heartbeat.
func HeartbeatRunner(ctx context.Context, tx pgx.Tx, runID string) error {
	if _, err := tx.Exec(ctx, `UPDATE runner_runs SET heartbeat_at = now() WHERE id = $1`, runID); err != nil {
		return fmt.Errorf("store: runner heartbeat: %w", err)
	}
	return nil
}

// SetRunnerPhase moves the run to phase.
func SetRunnerPhase(ctx context.Context, tx pgx.Tx, runID, phase string) error {
	tag, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = $2 WHERE id = $1`, runID, phase)
	if err != nil {
		return fmt.Errorf("store: set runner phase: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordRunnerFailure accepts an already redacted error. Only the executor can
// write finished_at. A runner can report its phase and error alone.
func RecordRunnerFailure(ctx context.Context, tx pgx.Tx, runID, cause string) error {
	if _, err := tx.Exec(ctx, `UPDATE runner_runs SET phase = 'failed', error = left($2, 2000) WHERE id = $1`, runID, cause); err != nil {
		return fmt.Errorf("store: record runner failure: %w", err)
	}
	return nil
}

// WriteRunnerContext commits the context and its next phase together. Account
// identity is copied from the authorized run, never supplied.
func WriteRunnerContext(ctx context.Context, tx pgx.Tx, runID string, p runspec.ContextPack, phase string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO context_packs (runner_run_id, account_id, head_sha, base_sha, patch_id, diff, changed_paths, stages, repo_files, repo_notes,
			prior_head_sha, delta_diff, delta_paths, scope, scope_reason, skip_reason, rule_ids, skip_detail)
		SELECT id, account_id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17 FROM runner_runs WHERE id = $1
		ON CONFLICT (runner_run_id) DO NOTHING`,
		runID, p.Head, p.Base, p.PatchID, p.Diff, p.ChangedPaths, p.Stages, p.Files, p.Notes,
		p.PriorHead, p.DeltaDiff, p.DeltaPaths, p.Scope, p.ScopeReason, p.SkipReason, p.RuleIDs, p.SkipDetail)
	if err != nil {
		return fmt.Errorf("store: write runner context: %w", err)
	}
	return SetRunnerPhase(ctx, tx, runID, phase)
}

// WriteRunnerAgent commits the result, retained conversation, and phase
// together.
func WriteRunnerAgent(ctx context.Context, tx pgx.Tx, runID string, rec runspec.AgentRecord, phase string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO agent_runs (runner_run_id, account_id, stop_reason, result, steps, tool_calls, timeline,
			input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_usd, model, error, sources,
			skills_offered, skills_opened, commands_offered, commands_run, continued_from, parts,
			unpriced_steps, command_errors, command_counts, forced_by)
		SELECT id, account_id, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10, $11, $12, left($13, 2000), $14, $15, $16, $17, $18,
			nullif($19, '')::uuid, coalesce($20::jsonb, '[]'::jsonb), $21, $22, coalesce($23::jsonb, '{}'::jsonb), $24
		FROM runner_runs WHERE id = $1
		ON CONFLICT (runner_run_id) DO NOTHING`,
		runID, rec.Stop, rec.Result, rec.Steps, rec.ToolCalls, rec.Timeline,
		rec.Usage.Input, rec.Usage.CacheRead, rec.Usage.CacheWrite, rec.Usage.Output, rec.CostUSD, rec.Model, rec.Error,
		rec.Sources, rec.SkillsOffered, rec.SkillsOpened, rec.CommandsOffered, rec.CommandsRun, rec.ContinuedFrom, rec.Parts,
		rec.UnpricedSteps, rec.CommandErrors, rec.CommandCounts, rec.ForcedBy)
	if err != nil {
		return fmt.Errorf("store: write agent run: %w", err)
	}
	if rec.Conversation != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations (runner_run_id, account_id, session, conversation, tokens, model)
			SELECT id, account_id, $2, $3, $4, $5 FROM runner_runs WHERE id = $1
			ON CONFLICT (runner_run_id) DO NOTHING`,
			runID, rec.Session, string(rec.Conversation), rec.Tokens, rec.GrantedModel); err != nil {
			return fmt.Errorf("store: write conversation: %w", err)
		}
	}
	return SetRunnerPhase(ctx, tx, runID, phase)
}

// BeginRunnerIndex opens an index upload, and returns the id that its batches
// and its seal must name. A new upload makes every earlier one stale, whatever
// is still on its way from it. It reports whether the run's pack is already
// written, so the upload is one that landed and lost its answer. Otherwise it
// clears what an upload that failed left. Staging holds no transaction across
// an upload, so a slow runner cannot pin a pooled connection. Nothing reads
// staging until the pack exists.
func BeginRunnerIndex(ctx context.Context, tx pgx.Tx, runID string) (upload string, written bool, err error) {
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM index_packs WHERE runner_run_id = $1)`, runID).Scan(&written); err != nil {
		return "", false, fmt.Errorf("store: read index pack: %w", err)
	}
	if written {
		return "", true, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM index_staging WHERE runner_run_id = $1`, runID); err != nil {
		return "", false, fmt.Errorf("store: clear index staging: %w", err)
	}
	if err := tx.QueryRow(ctx, `UPDATE runner_runs SET index_upload = gen_random_uuid() WHERE id = $1 RETURNING index_upload::text`, runID).
		Scan(&upload); err != nil {
		return "", false, fmt.Errorf("store: open index upload: %w", err)
	}
	return upload, false, nil
}

// checkRunnerUpload refuses an upload that a later one made stale. WithRunner
// holds the run's row, so the upload stays current for the rest of tx.
func checkRunnerUpload(ctx context.Context, tx pgx.Tx, runID, upload string) error {
	var current bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(index_upload = $2::uuid, false) FROM runner_runs WHERE id = $1`, runID, upload).
		Scan(&current); err != nil {
		return fmt.Errorf("store: read index upload: %w", err)
	}
	if !current {
		return ErrIndexStaging
	}
	return nil
}

// StageRunnerChunks stages one batch of an upload.
func StageRunnerChunks(ctx context.Context, tx pgx.Tx, runID, upload string, chunks []runspec.IndexChunk) error {
	if err := checkRunnerUpload(ctx, tx, runID, upload); err != nil {
		return err
	}
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

// FinishRunnerIndex seals an upload of count chunks: the pack and the done
// phase commit together, and only over what the upload staged.
func FinishRunnerIndex(ctx context.Context, tx pgx.Tx, runID, upload string, p runspec.IndexPack, count int) error {
	if err := checkRunnerUpload(ctx, tx, runID, upload); err != nil {
		return err
	}
	var staged int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM index_staging WHERE runner_run_id = $1`, runID).Scan(&staged); err != nil {
		return fmt.Errorf("store: count staged chunks: %w", err)
	}
	if staged != count {
		return fmt.Errorf("%w: %d chunks staged, %d uploaded", ErrIndexStaging, staged, count)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO index_packs (runner_run_id, account_id, base_sha, mode, changed_paths, chunk_count)
		SELECT id, account_id, $2, $3, $4, $5 FROM runner_runs WHERE id = $1`,
		runID, p.Base, p.Mode, p.ChangedPaths, count); err != nil {
		return fmt.Errorf("store: write runner index: %w", err)
	}
	return SetRunnerPhase(ctx, tx, runID, "done")
}
