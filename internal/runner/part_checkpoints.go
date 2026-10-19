package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/home-operations/kritika/internal/agent"
	"github.com/home-operations/kritika/internal/model"
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
// context invalidates the whole plan. Run identity and scheduling do not change
// the input a part reviews.
func partInputHash(p Spec, parts []reviewPart, tools agentTools) (string, error) {
	p.RunID = ""
	if p.Agent != nil {
		limits := *p.Agent
		limits.Parallel = 0
		p.Agent = &limits
	}
	type prompt struct {
		Paths        []string
		System, User string
		Submit       model.ToolDef
	}
	input := struct {
		Spec    Spec
		Prompts []prompt
		Tools   []model.ToolDef
	}{Spec: p}
	for _, part := range parts {
		input.Prompts = append(input.Prompts, prompt{part.paths, part.prompt.system, part.prompt.user, part.prompt.submit})
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

func loadParts(ctx context.Context, st *store.Store, p Spec, hash string, parts []reviewPart) ([]partCheckpoint, error) {
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
		if row.Part < 1 || row.Part > len(parts) {
			return nil, fmt.Errorf("runner: checkpoint part %d is outside the plan", row.Part)
		}
		part := &saved[row.Part-1]
		if err := json.Unmarshal(row.Payload, part); err != nil {
			return nil, fmt.Errorf("runner: decode part checkpoint: %w", err)
		}
		if part.Result.Stop != agent.StopSubmitted {
			return nil, fmt.Errorf("runner: checkpoint part %d is unfinished", row.Part)
		}
		if err := parts[row.Part-1].prompt.validate(part.Result.Submitted); err != nil {
			return nil, fmt.Errorf("runner: validate part checkpoint: %w", err)
		}
		part.SourceRun = row.SourceRun
	}
	return saved, nil
}

// savePart stores only a submitted answer and provenance. Usage stays on the
// source run; reusing an answer performs no model steps or commands.
func savePart(ctx context.Context, st *store.Store, p Spec, secrets Secrets, hash string, part int, res agent.Result,
	tools agentTools,
) error {
	meta := agentRecord{}
	meta.useTools(tools)
	checkpoint := partCheckpoint{
		Result:  agent.Result{Stop: res.Stop, Submitted: res.Submitted, Model: res.Model},
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
