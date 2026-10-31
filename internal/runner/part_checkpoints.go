package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/runspec"
	"github.com/home-operations/kritika/internal/store"
)

type partCheckpoint struct {
	Result        agent.Result
	Sources       []string
	SkillsOpened  []string
	CommandsRun   []string
	CommandErrors []string
	SourceRun     string
}

// partInputHash includes every part's prompt so a changed partition or retrieved
// context invalidates the whole plan. Run identity, scheduling, and the token
// budget the month left this attempt do not change the input a part reviews.
// Neither does the last review's conversation, which no part carries on.
func partInputHash(p runspec.Spec, parts []reviewPart, tools agentTools) (string, error) {
	p.RunID = ""
	if p.Agent != nil {
		limits := *p.Agent
		limits.Parallel, limits.MaxTokens = 0, 0
		p.Agent = &limits
	}
	if p.Prompt != nil {
		prompt := *p.Prompt
		prompt.Continue = nil
		p.Prompt = &prompt
	}
	type prompt struct {
		Paths        []string
		Reviewer     string
		System, User string
		Submit       model.ToolDef
	}
	input := struct {
		Spec    runspec.Spec
		Prompts []prompt
		Tools   []model.ToolDef
	}{Spec: p}
	for _, part := range parts {
		input.Prompts = append(input.Prompts, prompt{part.paths, part.reviewer, part.prompt.system, part.prompt.user, part.prompt.submit})
	}
	for _, tool := range tools.extra() {
		input.Tools = append(input.Tools, tool.Def())
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("runner: encode part inputs: %w", err)
	}
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:]), nil
}

// loadParts reads the parts the run can reuse. A checkpoint that cannot be
// reused is left out, so its part runs again rather than failing every
// retry that copies it.
func loadParts(
	ctx context.Context, st *store.Store, p runspec.Spec, hash string, parts []reviewPart, logger *slog.Logger,
) ([]partCheckpoint, error) {
	var rows []store.PartCheckpoint
	err := st.WithRunnerJob(ctx, p.RunID, func(tx pgx.Tx) error {
		var err error
		rows, err = store.ReadPartCheckpoints(ctx, tx, p.RunID, hash)
		return err
	})
	if err != nil {
		return nil, err
	}
	saved := make([]partCheckpoint, len(parts))
	for _, row := range rows {
		part, err := restorePart(row, parts)
		if err != nil {
			logger.Warn("agent part checkpoint not reused", "part", row.Part, "error", err)
			continue
		}
		saved[row.Part-1] = part
	}
	return saved, nil
}

// restorePart decodes a checkpoint as the answer of its part: one its
// submission check refuses is reused only if the answer was taken on the
// check's fallback, as the agent took it.
func restorePart(row store.PartCheckpoint, parts []reviewPart) (partCheckpoint, error) {
	var part partCheckpoint
	if row.Part < 1 || row.Part > len(parts) {
		return part, errors.New("runner: checkpoint part is outside the plan")
	}
	if err := json.Unmarshal(row.Payload, &part); err != nil {
		return part, fmt.Errorf("runner: decode part checkpoint: %w", err)
	}
	if part.Result.Stop != agent.StopSubmitted {
		return part, errors.New("runner: checkpoint part is unfinished")
	}
	prompt := parts[row.Part-1].prompt
	if err := prompt.validate(part.Result.Submitted); err != nil && (prompt.fallback == nil || !prompt.fallback(part.Result.Submitted)) {
		return part, fmt.Errorf("runner: validate part checkpoint: %w", err)
	}
	part.SourceRun = row.SourceRun
	return part, nil
}

// savePart stores only a submitted answer, the limit that forced it, and
// provenance. Usage stays on the source run. Reusing an answer performs no
// model steps or commands.
func savePart(ctx context.Context, st *store.Store, p runspec.Spec, secrets runspec.Secrets, hash string, part int, res agent.Result,
	tools agentTools,
) error {
	meta := agentRecord{}
	meta.useTools(tools)
	checkpoint := partCheckpoint{
		Result:  agent.Result{Stop: res.Stop, Submitted: res.Submitted, Model: res.Model, ForcedBy: res.ForcedBy},
		Sources: toolSources(tools), SkillsOpened: meta.skillsOpened, CommandsRun: meta.commandsRun, CommandErrors: meta.commandErrors,
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("runner: encode part checkpoint: %w", err)
	}
	return st.WithRunnerJob(ctx, p.RunID, func(tx pgx.Tx) error {
		return store.SavePartCheckpoint(ctx, tx, p.RunID, hash, part, json.RawMessage(secrets.Mask(string(payload))))
	})
}

func checkpointNames(current, saved []string) []string {
	for _, name := range saved {
		if !slices.Contains(current, name) {
			current = append(current, name)
		}
	}
	return current
}
