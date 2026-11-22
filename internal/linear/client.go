package linear

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Endpoint is Linear's GraphQL API.
const Endpoint = "https://api.linear.app/graphql"

// ErrNotFound is the error for an issue that Linear does not give: one that
// does not exist, or one that the key cannot read.
var ErrNotFound = errors.New("linear: issue not found")

// maxResponseBytes bounds the answer read for one issue. The prompt keeps a
// few thousand bytes of its description.
const maxResponseBytes = 1 << 20

// issueQuery reads an issue by its UUID or its ID, such as ENG-123.
const issueQuery = `query Issue($id: String!) { issue(id: $id) { identifier title description team { key } } }`

// Issue is a Linear issue as the prompt shows it.
type Issue struct {
	// ID is the issue's current ID, such as ENG-123. It changes when the
	// issue moves to another team.
	ID          string
	Title       string
	Description string
	// Team is the key of the issue's team.
	Team string
}

// Client reads issues through Linear's GraphQL API. Its zero value calls
// Endpoint with defaultHTTP.
type Client struct {
	URL  string
	HTTP *http.Client
}

var defaultHTTP = &http.Client{Timeout: 15 * time.Second}

// Issue reads the issue whose ID is id with the personal API key key.
func (c *Client) Issue(ctx context.Context, key, id string) (Issue, error) {
	body, err := json.Marshal(map[string]any{"query": issueQuery, "variables": map[string]string{"id": id}})
	if err != nil {
		return Issue{}, fmt.Errorf("linear: issue %s: %w", id, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cmp.Or(c.URL, Endpoint), bytes.NewReader(body))
	if err != nil {
		return Issue{}, fmt.Errorf("linear: issue %s: %w", id, err)
	}
	// A personal API key goes in the header as it is, with no scheme.
	req.Header.Set("Authorization", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cmp.Or(c.HTTP, defaultHTTP).Do(req)
	if err != nil {
		return Issue{}, fmt.Errorf("linear: issue %s: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// A query that fails, such as one for an issue that the key cannot
	// read, comes back with errors under a 200 or a 400.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return Issue{}, fmt.Errorf("linear: issue %s: %s", id, resp.Status)
	}
	var out struct {
		Data struct {
			Issue *struct {
				Identifier  string
				Title       string
				Description *string
				Team        struct{ Key string }
			}
		}
		Errors []struct {
			Message    string
			Extensions struct{ Type string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return Issue{}, fmt.Errorf("linear: issue %s: %s: %w", id, resp.Status, err)
	}
	if len(out.Errors) > 0 {
		e := out.Errors[0]
		// Linear types the lookup of an issue that it does not give as
		// invalid input.
		if e.Extensions.Type == "invalid input" {
			return Issue{}, fmt.Errorf("linear: issue %s: %s: %w", id, e.Message, ErrNotFound)
		}
		return Issue{}, fmt.Errorf("linear: issue %s: %s (%s)", id, e.Message, e.Extensions.Type)
	}
	is := out.Data.Issue
	if is == nil {
		return Issue{}, fmt.Errorf("linear: issue %s: %w", id, ErrNotFound)
	}
	var description string
	if is.Description != nil {
		description = *is.Description
	}
	return Issue{ID: is.Identifier, Title: is.Title, Description: description, Team: is.Team.Key}, nil
}
