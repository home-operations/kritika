package runner

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runspec"
)

const (
	shaA = "0123456789abcdef0123456789abcdef01234567"
	shaB = "89abcdef0123456789abcdef0123456789abcdef"
)

func reviewSpec() runspec.Spec {
	return runspec.Spec{
		Version: runspec.Version, Kind: runspec.KindReview, RunID: "run-1", CloneURL: "https://forge.example.com/acme/widgets.git",
		GatewayURL: "http://kritika-gateway:8082",
		Head:       shaA, Base: shaB, Ignore: []string{"vendor/**"}, RepoFiles: []string{"docs/rules.md"},
		Agent: &runspec.AgentLimits{MaxSteps: 30, MaxToolOutputBytes: 16 << 10, MaxTokens: 200000},
		Model: &runspec.ModelEndpoint{Model: "review"},
		Prompt: &runspec.Prompt{
			Repository: "acme/widgets",
			PullRequest: repoconfig.PullRequest{Number: 7, Title: "Add b", Author: "octocat", Body: "Adds b.", BaseRef: "main", State: "open",
				Labels: []byte(`[{"name":"deps"}]`)},
			MaxDeltaFiles: 25, Prior: []review.Finding{{Path: "main.go", Line: 1, Severity: review.SeverityP2, Title: "earlier finding"}}},
	}
}

// followUpSpec is reviewSpec as a follow-up's: the same agent, which answers
// a thread.
func followUpSpec() runspec.Spec {
	s := reviewSpec()
	s.Kind = runspec.KindFollowUp
	s.Thread = []review.Message{review.NewMessage("octocat", "@kritika why b?", time.Date(2026, 10, 6, 19, 58, 0, 0, time.UTC))}
	return s
}

func TestHeartbeatBeatsOnInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = time.Second
		var beats atomic.Int32
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			heartbeat(ctx, interval, func(context.Context) error {
				beats.Add(1)
				return nil
			}, slog.New(slog.DiscardHandler))
			close(done)
		}()
		synctest.Wait()
		if n := beats.Load(); n != 1 {
			t.Fatalf("beats = %d at start, want 1 (one immediately)", n)
		}
		synctest.Sleep(3 * interval)
		if n := beats.Load(); n != 4 {
			t.Fatalf("beats = %d after 3 intervals, want 4", n)
		}
		cancel()
		<-done
		synctest.Sleep(3 * interval)
		if n := beats.Load(); n != 4 {
			t.Fatalf("beats = %d after cancel, want 4: heartbeat kept beating after its context ended", n)
		}
	})
}
