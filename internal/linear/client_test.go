package linear

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIssue(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		answer   string
		want     Issue
		notFound bool
		wantErr  string
	}{
		{name: "an issue", status: http.StatusOK,
			answer: `{"data":{"issue":{"identifier":"ENG-12","title":"Widgets leak","description":"Steps.","team":{"key":"ENG","organization":{"urlKey":"acme"}}}}}`,
			want:   Issue{ID: "ENG-12", Title: "Widgets leak", Description: "Steps.", Team: "ENG", Workspace: "acme"}},
		{name: "an issue with no description", status: http.StatusOK,
			answer: `{"data":{"issue":{"identifier":"ENG-12","title":"Widgets leak","description":null,"team":{"key":"ENG"}}}}`,
			want:   Issue{ID: "ENG-12", Title: "Widgets leak", Team: "ENG"}},
		{name: "not found under a 200", status: http.StatusOK,
			answer:   `{"data":null,"errors":[{"message":"Entity not found: Issue","extensions":{"type":"invalid input","userError":true}}]}`,
			notFound: true},
		{name: "not found under a 400", status: http.StatusBadRequest,
			answer:   `{"errors":[{"message":"Entity not found: Issue","extensions":{"type":"invalid input"}}]}`,
			notFound: true},
		{name: "an issue given as null", status: http.StatusOK, answer: `{"data":{"issue":null}}`, notFound: true},
		{name: "a 400 with no errors", status: http.StatusBadRequest, answer: `{}`, wantErr: "the answer holds no issue"},
		{name: "no issue in the data", status: http.StatusOK, answer: `{"data":{}}`, wantErr: "the answer holds no issue"},
		{name: "a 400 with an issue", status: http.StatusBadRequest, answer: `{"data":{"issue":null}}`, wantErr: "the answer holds no issue"},
		{name: "a key Linear refuses", status: http.StatusBadRequest,
			answer:  `{"errors":[{"message":"Authentication required, not authenticated","extensions":{"type":"authentication error"}}]}`,
			wantErr: "Authentication required, not authenticated (authentication error)"},
		{name: "rate limited", status: http.StatusTooManyRequests,
			answer: `{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED"}}]}`, wantErr: "429 Too Many Requests"},
		{name: "a server error", status: http.StatusBadGateway, answer: `bad gateway`, wantErr: "502 Bad Gateway"},
		{name: "not JSON", status: http.StatusOK, answer: `<html>`, wantErr: "200 OK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Query     string
					Variables map[string]string
				}
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "lin_api_key" ||
					r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request = %s with Authorization %q, Content-Type %q", r.Method, r.Header.Get("Authorization"),
						r.Header.Get("Content-Type"))
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Variables["id"] != "ENG-12" ||
					!strings.Contains(req.Query, "issue(id: $id)") {
					t.Errorf("request body = %+v, %v; want the issue query for ENG-12", req, err)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.answer))
			}))
			defer srv.Close()
			c := &Client{URL: srv.URL}
			got, err := c.Issue(t.Context(), "lin_api_key", "ENG-12")
			switch {
			case tt.notFound:
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
			case tt.wantErr != "":
				if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one with %q that is not ErrNotFound", err, tt.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case got != tt.want:
				t.Fatalf("Issue = %+v, want %+v", got, tt.want)
			}
		})
	}
}
