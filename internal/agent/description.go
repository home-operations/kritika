package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/textcut"
)

var readDescriptionSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"issue": {"type": "string",
			"description": "ID of a linked issue the prompt lists, such as #12 or ENG-123, to read its body instead of the description."}
	},
	"additionalProperties": false
}`)

type readDescriptionTool struct {
	body     string
	issues   map[string]string
	maxBytes int
}

// ReadDescriptionTool returns the pull request description whole, or the body
// of one of the linked issues, by its ID in issues. The prompt shows each of
// these only up to its share of the budget. The tool caps its output at
// maxBytes.
func ReadDescriptionTool(body string, issues map[string]string, maxBytes int) Tool {
	return &readDescriptionTool{body: body, issues: issues, maxBytes: maxBytes}
}

func (dt *readDescriptionTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "read_description",
		Description: "Read the whole pull request description, or a linked issue's body by its ID, where the prompt shows it cut.",
		InputSchema: readDescriptionSchema,
	}
}

func (dt *readDescriptionTool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Issue string `json:"issue"`
	}
	if err := decodeInput(input, &req); err != nil {
		return "", fmt.Errorf("agent: read_description: %w", err)
	}
	text := dt.body
	if id := issueID(req.Issue); id != "" {
		var ok bool
		if text, ok = dt.issues[id]; !ok {
			return "", fmt.Errorf("agent: read_description: issue %s is not one the prompt lists", id)
		}
	}
	if text = strings.TrimSpace(text); text == "" {
		return "(empty)", nil
	}
	return textcut.Truncate(text, dt.maxBytes), nil
}

// issueID is the ID that the prompt shows for the issue that the model asked
// for as id: a forge issue's number takes its "#", and a Linear ID is in
// upper case.
func issueID(id string) string {
	id = strings.ToUpper(strings.TrimSpace(id))
	if id != "" && strings.Trim(id, "0123456789") == "" {
		return "#" + id
	}
	return id
}
