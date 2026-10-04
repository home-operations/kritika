package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// threadsAPI answers the review threads query with pages of threads and
// records the resolve mutations it gets.
type threadsAPI struct {
	pages    []string
	resolved []string
}

func (a *threadsAPI) serve(f *fakeAPI) {
	f.mux.HandleFunc("POST /api/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := f.bodies["POST /api/graphql"].(map[string]any)
		query, _ := body["query"].(string)
		vars, _ := body["variables"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(query, "resolveReviewThread") {
			id, _ := vars["id"].(string)
			a.resolved = append(a.resolved, id)
			_, _ = fmt.Fprintf(w, `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`)
			return
		}
		page := 0
		if after, _ := vars["after"].(string); after != "" {
			_, _ = fmt.Sscanf(after, "page%d", &page)
		}
		if page >= len(a.pages) {
			_, _ = fmt.Fprintf(w, `{"errors":[{"message":"no such page"}]}`)
			return
		}
		next := `"hasNextPage":false,"endCursor":null`
		if page+1 < len(a.pages) {
			next = fmt.Sprintf(`"hasNextPage":true,"endCursor":"page%d"`, page+1)
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{%s},"nodes":%s}}}}}`, next, a.pages[page])
	})
}

func thread(id string, resolved bool, comments ...string) string {
	nodes := make([]string, 0, len(comments))
	for _, c := range comments {
		login, dbID, _ := strings.Cut(c, "#")
		nodes = append(nodes, fmt.Sprintf(`{"databaseId":%s,"author":{"login":%q}}`, dbID, login))
	}
	return fmt.Sprintf(`{"id":%q,"isResolved":%v,"comments":{"nodes":[%s]}}`, id, resolved, strings.Join(nodes, ","))
}

func TestResolveThread(t *testing.T) {
	tests := []struct {
		name         string
		pages        []string
		id           int64
		anyAuthor    bool
		want         bool
		wantResolved []string
	}{
		{
			name:  "the bot's open thread is resolved",
			pages: []string{"[" + thread("T1", false, "kritika#11") + "," + thread("T2", false, "kritika#21", "kritika#22") + "]"},
			id:    21, want: true, wantResolved: []string{"T2"},
		},
		{
			name:  "a thread someone else wrote in is left open",
			pages: []string{"[" + thread("T1", false, "kritika#11", "devin#12") + "]"},
			id:    11,
		},
		{
			name:  "a thread someone else wrote in is resolved when asked",
			pages: []string{"[" + thread("T1", false, "kritika#11", "devin#12") + "]"},
			id:    11, anyAuthor: true, want: true, wantResolved: []string{"T1"},
		},
		{
			name:  "a thread already resolved is left alone",
			pages: []string{"[" + thread("T1", true, "kritika#11") + "]"},
			id:    11,
		},
		{
			name:  "a comment in no thread resolves nothing",
			pages: []string{"[" + thread("T1", false, "kritika#11") + "]"},
			id:    99,
		},
		{
			name:  "a thread on a later page is found",
			pages: []string{"[" + thread("T1", false, "kritika#11") + "]", "[" + thread("T2", false, "kritika[bot]#21") + "]"},
			id:    21, want: true, wantResolved: []string{"T2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c := newFakeAPI(t)
			c.login = "kritika[bot]"
			api := &threadsAPI{pages: tt.pages}
			api.serve(f)
			got, err := c.ResolveThread(t.Context(), "o", "r", 7, tt.id, !tt.anyAuthor)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ResolveThread() = %v, want %v", got, tt.want)
			}
			if strings.Join(api.resolved, ",") != strings.Join(tt.wantResolved, ",") {
				t.Errorf("resolved threads = %v, want %v", api.resolved, tt.wantResolved)
			}
			vars, _ := f.bodies["POST /api/graphql"].(map[string]any)["variables"].(map[string]any)
			if tt.want {
				if vars["id"] != tt.wantResolved[0] {
					t.Errorf("mutation variables = %v", vars)
				}
			} else if vars["owner"] != "o" || vars["repo"] != "r" || vars["number"] != float64(7) {
				t.Errorf("query variables = %v", vars)
			}
		})
	}
}

// TestResolveThreadWithoutContentsWrite: GitHub refuses the mutation to an
// installation that cannot write contents, so the client does not ask.
func TestResolveThreadWithoutContentsWrite(t *testing.T) {
	tests := []struct {
		name        string
		permissions string
		want        bool
	}{
		{"contents read resolves nothing", `,"permissions":{"contents":"read","pull_requests":"write"}`, false},
		{"contents write resolves", `,"permissions":{"contents":"write","pull_requests":"write"}`, true},
		{"permissions unsaid resolves", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graphql := 0
			_, pemKey := testKeyPEM(t)
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v3/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"token":"ghs_test","expires_at":%q%s}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), tt.permissions)
			})
			mux.HandleFunc("POST /api/graphql", func(w http.ResponseWriter, r *http.Request) {
				graphql++
				w.Header().Set("Content-Type", "application/json")
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "resolveReviewThread") {
					_, _ = fmt.Fprint(w, `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`)
					return
				}
				_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[%s]}}}}}`, thread("T1", false, "kritika#11"))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			app, err := NewApp("Iv1.abc", pemKey, srv.URL+"/api/v3")
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewClient(app, 42)
			if err != nil {
				t.Fatal(err)
			}
			c.login = "kritika[bot]"
			got, err := c.ResolveThread(t.Context(), "o", "r", 7, 11, true)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ResolveThread() = %v, want %v", got, tt.want)
			}
			if !tt.want && graphql != 0 {
				t.Errorf("%d GraphQL requests, want none", graphql)
			}
		})
	}
}

func TestResolveThreadErrors(t *testing.T) {
	f, c := newFakeAPI(t)
	c.login = "kritika[bot]"
	f.mux.HandleFunc("POST /api/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "Could not resolve to a PullRequest"}}})
	})
	if _, err := c.ResolveThread(t.Context(), "o", "r", 7, 11, true); err == nil || !strings.Contains(err.Error(), "Could not resolve") {
		t.Fatalf("ResolveThread() error = %v, want the GraphQL error", err)
	}
}

func TestGraphqlURL(t *testing.T) {
	for _, tt := range []struct{ apiBase, want string }{
		{"", "https://api.github.com/graphql"},
		{"https://ghe.example.com", "https://ghe.example.com/api/graphql"},
		{"https://ghe.example.com/api/v3", "https://ghe.example.com/api/graphql"},
	} {
		api, err := newClient(http.DefaultTransport, tt.apiBase)
		if err != nil {
			t.Fatal(err)
		}
		if got := (&Client{api: api}).graphqlURL(); got != tt.want {
			t.Errorf("graphqlURL(%q) = %q, want %q", tt.apiBase, got, tt.want)
		}
	}
}
