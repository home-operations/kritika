//go:build integration

package worker

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/jobs"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
	"github.com/home-operations/kritika/internal/store/storetest"
)

// postHookForge runs onPost once, as the next comment is posted, after the
// forge creates it: it is given the context the post was made on, and its
// error is the post's. It finds no .kritika.yaml.
type postHookForge struct {
	*localForge
	onPost func(ctx context.Context) error
}

func (f *postHookForge) CreateComment(ctx context.Context, owner, repo string, number int, body string) (int64, error) {
	id, err := f.localForge.CreateComment(ctx, owner, repo, number, body)
	if fn := f.onPost; fn != nil && err == nil {
		f.onPost = nil
		return id, fn(ctx)
	}
	return id, err
}

func (f *postHookForge) FileAt(context.Context, string, string, string, string) ([]byte, error) {
	return nil, fs.ErrNotExist
}

// mentions is a pull request on which onedr0p, who can write, made each of
// comments. answered of its follow-ups were answered in the last hour.
type mentions struct {
	t       *testing.T
	st      *store.Store
	account *configfile.Account
	prID    string
	forge   *postHookForge
	w       *FollowUp
	args    jobs.FollowUpArgs
}

func newMentions(t *testing.T, number, answered int, comments ...string) *mentions {
	t.Helper()
	ctx := t.Context()
	st := storetest.Open(t)
	t.Setenv("TEST_PEM", "pem")
	t.Setenv("TEST_SECRET", "test-provider-key")
	file := configfiletest.Load(t, configYAML)
	if err := st.ApplyConfig(ctx, file); err != nil {
		t.Fatal(err)
	}
	storetest.Repositories(t, st, file, "onedr0p/home-ops")
	account, _ := file.Account(configfile.ForgeGitHub, "onedr0p")
	repoID := configfile.RepositoryID(account.ID(), "onedr0p/home-ops")
	m := &mentions{t: t, st: st, account: account, args: jobs.FollowUpArgs{AccountID: account.ID(), RepositoryID: repoID, Number: number}}
	if err := st.WithAccount(ctx, account.ID(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO pull_requests (account_id, repository_id, number, head_sha)
			VALUES ($1, $2, $3, 'head') RETURNING id`, account.ID(), repoID, number).Scan(&m.prID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO followups (account_id, pull_request_id, comment_id, status)
			SELECT $1, $2, s, 'answered' FROM generate_series(1, $3) AS s`, account.ID(), m.prID, answered)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.WithAccount(context.Background(), account.ID(), func(tx pgx.Tx) error {
			for _, query := range []string{
				`DELETE FROM followups WHERE pull_request_id = $1`,
				`DELETE FROM pull_requests WHERE id = $1`,
			} {
				if _, err := tx.Exec(context.Background(), query, m.prID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Errorf("clean up the mentions' pull request: %v", err)
		}
	})
	lf := &localForge{permissions: map[string]forge.Permission{"onedr0p": forge.PermissionWrite}}
	for _, body := range comments {
		lf.addComment("onedr0p", body)
	}
	m.forge = &postHookForge{localForge: lf}
	m.w = &FollowUp{Store: st, Current: configfile.NewCurrent(file), Forges: &forges{f: m.forge}, Logger: slog.New(slog.DiscardHandler)}
	return m
}

// work handles the mention made by the comment-th comment, as its job's
// attempt.
func (m *mentions) work(ctx context.Context, comment, attempt int) error {
	args := m.args
	args.CommentID = commentBase + int64(comment)
	job := &river.Job[jobs.FollowUpArgs]{
		JobRow: &rivertype.JobRow{ID: args.CommentID, Attempt: attempt, MaxAttempts: 8, CreatedAt: time.Now()}, Args: args,
	}
	return m.w.Work(ctx, job)
}

// posted counts the bot's comments that contain s.
func (m *mentions) posted(s string) int {
	m.forge.mu.Lock()
	defer m.forge.mu.Unlock()
	n := 0
	for id, body := range m.forge.comments {
		if m.forge.authors[id] == "kritika[bot]" && strings.Contains(body, s) {
			n++
		}
	}
	return n
}

// status is the recorded outcome of the comment-th comment's mention.
func (m *mentions) status(comment int) (store.FollowupStatus, string) {
	m.t.Helper()
	var state store.FollowupState
	if err := m.st.WithAccount(m.t.Context(), m.account.ID(), func(tx pgx.Tx) error {
		var err error
		state, err = store.FindFollowup(m.t.Context(), tx, m.prID, commentBase+int64(comment), false)
		return err
	}); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.t.Fatal(err)
	}
	return state.Status, state.Reason
}

// TestFollowUpLimitNoticeOnce: once a pull request got its answers for the
// hour, the limit notice is posted once. That holds whether a second mention
// is handled while the first posts it or the first is retried after posting.
func TestFollowUpLimitNoticeOnce(t *testing.T) {
	t.Run("a second mention while the first posts", func(t *testing.T) {
		m := newMentions(t, 4401, followUpsPerHour, "@kritika why?", "@kritika and this?")
		var second error
		m.forge.onPost = func(context.Context) error {
			second = m.work(t.Context(), 2, 1)
			return nil
		}
		if err := m.work(t.Context(), 1, 1); err != nil {
			t.Fatal(err)
		}
		if second != nil {
			t.Fatal(second)
		}
		if n := m.posted("limit of follow-ups"); n != 1 {
			t.Fatalf("limit notices = %d, want 1", n)
		}
		for comment := 1; comment <= 2; comment++ {
			if status, _ := m.status(comment); status != store.FollowupLimited {
				t.Fatalf("mention %d = %s, want limited", comment, status)
			}
		}
	})
	t.Run("a retry after the post was cut", func(t *testing.T) {
		m := newMentions(t, 4402, followUpsPerHour, "@kritika why?")
		ctx, cancel := context.WithCancel(t.Context())
		m.forge.onPost = func(context.Context) error {
			cancel()
			return nil
		}
		_ = m.work(ctx, 1, 1)
		if err := m.work(t.Context(), 1, 2); err != nil {
			t.Fatal(err)
		}
		if n := m.posted("limit of follow-ups"); n != 1 {
			t.Fatalf("limit notices = %d, want 1", n)
		}
	})
}

// TestFollowUpCommandReplyOnce: a command's reply cut after the forge created
// it, as a stopping worker cuts it, is still recorded. So the job's rerun,
// its attempt refunded, does not post it again.
func TestFollowUpCommandReplyOnce(t *testing.T) {
	m := newMentions(t, 4403, 0, "@kritika pause")
	ctx, cancel := context.WithCancel(t.Context())
	m.forge.onPost = func(post context.Context) error {
		cancel()
		return post.Err()
	}
	_ = m.work(ctx, 1, 1)
	if err := m.work(t.Context(), 1, 1); err != nil {
		t.Fatal(err)
	}
	if n := m.posted(review.FollowUpMarker(commentBase + 1)); n != 1 {
		t.Fatalf("replies = %d, want 1", n)
	}
}

// TestFollowUpFailureRecordedPastTheJob: a mention whose job ended as it
// failed is recorded failed all the same.
func TestFollowUpFailureRecordedPastTheJob(t *testing.T) {
	m := newMentions(t, 4404, 0, "@kritika pause")
	ctx, cancel := context.WithCancel(t.Context())
	m.forge.onPost = func(context.Context) error {
		cancel()
		return errors.New("forge refused the reply")
	}
	if err := m.work(ctx, 1, 1); err == nil {
		t.Fatal("the refused reply did not fail the follow-up")
	}
	if status, reason := m.status(1); status != store.FollowupFailed || !strings.Contains(reason, "forge refused") {
		t.Fatalf("mention = %s (%s), want failed", status, reason)
	}
}

// TestFollowUpReplyCutByAFence: a reply that a fence cuts, after River
// stopped the job, fails the job with the fence rather than the cancellation.
// River will otherwise take the cancellation for its stop and refund the
// attempt for it.
func TestFollowUpReplyCutByAFence(t *testing.T) {
	m := newMentions(t, 4405, 0, "@kritika pause")
	fenced, fence := context.WithCancelCause(context.Background())
	defer fence(nil)
	ctx, stop := context.WithCancel(context.WithValue(t.Context(), jobFenceKey{}, fenced))
	defer stop()
	m.forge.onPost = func(post context.Context) error {
		stop()
		fence(errJobFenced)
		<-post.Done()
		return post.Err()
	}
	if err := m.work(ctx, 1, 1); !errors.Is(err, errJobFenced) || errors.Is(err, context.Canceled) {
		t.Fatalf("Work = %v, want the fence and no cancellation", err)
	}
}
