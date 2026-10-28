package worker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/store"
)

// threadForge answers the comment listings from fixed comments and counts
// the inline ones, and answers every permission lookup the same way.
type threadForge struct {
	forge.Client
	inline       []forge.Comment
	conversation []forge.Comment
	inlineLists  int
	permission   forge.Permission
	permErr      error
	// threads are the inline comments GetComment serves, by id, and
	// threadStates whether the thread a root opened is resolved.
	threads        map[int64]forge.Comment
	threadStates   map[int64]bool
	threadStateErr error
}

func (f *threadForge) Permission(context.Context, string, string, string) (forge.Permission, error) {
	return f.permission, f.permErr
}

func (f *threadForge) GetComment(_ context.Context, _, _ string, _ int, id int64, _ bool) (forge.Comment, error) {
	if c, ok := f.threads[id]; ok {
		return c, nil
	}
	return forge.Comment{}, errors.New("no such comment")
}

func (f *threadForge) ThreadResolved(_ context.Context, _, _ string, _ int, id int64) (bool, bool, error) {
	resolved, found := f.threadStates[id]
	return resolved, found, f.threadStateErr
}

func (f *threadForge) ListInline(context.Context, string, string, int) ([]forge.Comment, error) {
	f.inlineLists++
	return f.inline, nil
}

func (f *threadForge) ListConversation(context.Context, string, string, int) ([]forge.Comment, error) {
	return f.conversation, nil
}

func TestFollowUpThread(t *testing.T) {
	at := func(s int64) time.Time { return time.Unix(s, 0) }
	root := forge.Comment{ID: 1, Body: "@kritika is this safe?", CreatedAt: at(1), Inline: true}
	reply := forge.Comment{ID: 2, Body: "@kritika why?", CreatedAt: at(2), Inline: true, InReplyTo: 1}
	other := forge.Comment{ID: 3, Body: "another thread", CreatedAt: at(3), Inline: true}
	later := forge.Comment{ID: 4, Body: "a later reply", CreatedAt: at(4), Inline: true, InReplyTo: 1}
	first := forge.Comment{ID: 10, Body: "first", CreatedAt: at(5)}
	asking := forge.Comment{ID: 11, Body: "@kritika summarize", CreatedAt: at(6)}
	tests := []struct {
		name       string
		comment    forge.Comment
		want       []string
		wantListed int
	}{
		{name: "a reply gathers its thread, the asking comment last", comment: reply,
			want: []string{root.Body, later.Body, reply.Body}, wantListed: 1},
		{name: "a comment that starts its thread lists nothing", comment: root, want: []string{root.Body}},
		{name: "a conversation comment gathers the conversation", comment: asking, want: []string{first.Body, asking.Body}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &threadForge{inline: []forge.Comment{root, reply, other, later}, conversation: []forge.Comment{first, asking}}
			f := &followUp{client: client, owner: "o", repo: "r", pr: &pullRequest{number: 7}, comment: tt.comment}
			msgs, _, err := f.thread(t.Context())
			if err != nil {
				t.Fatalf("thread: %v", err)
			}
			var got []string
			for _, m := range msgs {
				got = append(got, m.Body)
			}
			if !slices.Equal(got, tt.want) || client.inlineLists != tt.wantListed {
				t.Fatalf("thread = %q with %d inline listings, want %q with %d", got, client.inlineLists, tt.want, tt.wantListed)
			}
		})
	}
}

func TestRequestsReview(t *testing.T) {
	for _, tt := range []struct {
		body string
		want bool
	}{
		{"@kritika review", true},
		{"Looks fine to me. @Kritika Review please", true},
		{"@kritika review?", true},
		{"@kritika  review\nthe auth change", true},
		{"@other review security @kritika review", true},
		{"@kritika reviewed this already?", false},
		{"@kritika why is b here? a review would help", false},
		{"@kritikabot review", false},
		{"someone@kritika review", false},
		{"review @kritika", false},
	} {
		if got := requestsReview(tt.body, "kritika"); got != tt.want {
			t.Errorf("requestsReview(%q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}

func TestMarkedReply(t *testing.T) {
	const login = "kritika[bot]"
	reply := func(id int64, author string, commentID int64) forge.Comment {
		return forge.Comment{ID: id, Author: author, Body: "Here is why.\n\n" + review.FollowUpMarker(commentID)}
	}
	tests := []struct {
		name     string
		comments []forge.Comment
		want     int64
	}{
		{name: "the bot's reply to the comment is found", comments: []forge.Comment{reply(20, login, 7)}, want: 20},
		{name: "a reply to another comment is not", comments: []forge.Comment{reply(21, login, 8)}},
		{name: "another author's marker is not", comments: []forge.Comment{reply(22, "mallory", 7)}},
		{name: "no comments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := markedReply(tt.comments, login, 7); got.ID != tt.want {
				t.Errorf("markedReply = %+v, want id %d", got, tt.want)
			}
		})
	}
}

func TestDisqualified(t *testing.T) {
	const root, other = int64(70), int64(71)
	threads := map[int64]forge.Comment{
		root:  {ID: root, Author: "kritika[bot]", Body: review.FindingMarker("fp1") + "\n**Unchecked error**", Inline: true},
		other: {ID: other, Author: "onedr0p", Body: "nit: rename this", Inline: true},
	}
	inReply := func(to int64, body string) forge.Comment {
		return forge.Comment{Author: "alice", Body: body, Inline: true, InReplyTo: to}
	}
	tests := []struct {
		name       string
		comment    forge.Comment
		permission forge.Permission
		permErr    error
		// asReply says the job allows a reply, replies that the repository
		// does, and resolved the thread's state, nil for one not found.
		asReply, replies bool
		resolved         *bool
		stateErr         error
		want             string
		wantFingerprint  string
	}{
		{name: "a bot author", comment: forge.Comment{Author: "renovate[bot]", AuthorIsBot: true, Body: "@kritika explain"}, want: "author is a bot"},
		{name: "the bot's own login, whatever its case", comment: forge.Comment{Author: "Kritika[Bot]", Body: "@kritika explain"}, want: "author is a bot"},
		{name: "no mention of the bot", comment: forge.Comment{Author: "alice", Body: "looks good"}, want: "does not mention @kritika"},
		{name: "a permission lookup that fails", comment: forge.Comment{Author: "alice", Body: "@kritika explain"},
			permErr: errors.New("boom"), want: "permission unknown"},
		{name: "read access", comment: forge.Comment{Author: "alice", Body: "@kritika explain"},
			permission: forge.PermissionRead, want: "author has read access, write is required"},
		{name: "write access", comment: forge.Comment{Author: "alice", Body: "@kritika explain"}, permission: forge.PermissionWrite},
		{name: "a reply where replies are off", comment: inReply(root, "the caller holds it"), asReply: true, resolved: new(false),
			permission: forge.PermissionWrite, want: "replies in finding threads are off for this repository"},
		{name: "a reply in someone else's thread", comment: inReply(other, "agreed"), asReply: true, replies: true, resolved: new(false),
			permission: forge.PermissionWrite, want: "not a reply in one of kritika's finding threads"},
		{name: "a reply in a resolved thread", comment: inReply(root, "the caller holds it"), asReply: true, replies: true, resolved: new(true),
			permission: forge.PermissionWrite, want: "thread is resolved"},
		{name: "a reply in a thread not found", comment: inReply(root, "the caller holds it"), asReply: true, replies: true,
			permission: forge.PermissionWrite, want: "thread not found"},
		{name: "a reply whose thread state is unknown", comment: inReply(root, "the caller holds it"), asReply: true, replies: true,
			stateErr: errors.New("boom"), permission: forge.PermissionWrite, want: "thread state unknown"},
		{name: "a reply to a comment that is gone", comment: inReply(72, "the caller holds it"), asReply: true, replies: true,
			permission: forge.PermissionWrite, want: "thread unknown"},
		{name: "a reply with read access", comment: inReply(root, "the caller holds it"), asReply: true, replies: true, resolved: new(false),
			permission: forge.PermissionRead, want: "author has read access, write is required"},
		{name: "a reply the job did not allow", comment: inReply(root, "the caller holds it"), replies: true, resolved: new(false),
			permission: forge.PermissionWrite, want: "does not mention @kritika"},
		{name: "a reply in the bot's finding thread", comment: inReply(root, "the caller holds it"), asReply: true, replies: true,
			resolved: new(false), permission: forge.PermissionWrite, wantFingerprint: "fp1"},
		{name: "a mention in the bot's finding thread is a mention", comment: inReply(root, "@kritika dismiss, the caller holds it"),
			asReply: true, replies: true, resolved: new(true), permission: forge.PermissionWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &threadForge{permission: tt.permission, permErr: tt.permErr, threads: threads, threadStateErr: tt.stateErr,
				threadStates: map[int64]bool{}}
			if tt.resolved != nil {
				client.threadStates[tt.comment.InReplyTo] = *tt.resolved
			}
			f := &followUp{client: client, owner: "o", repo: "r", pr: &pullRequest{number: 1}, comment: tt.comment, botLogin: "kritika[bot]",
				asReply: tt.asReply, logger: slog.New(slog.DiscardHandler)}
			f.settings.Review.Replies = tt.replies
			if got := f.disqualified(t.Context()); got != tt.want {
				t.Errorf("disqualified = %q, want %q", got, tt.want)
			}
			// A mention is answered as one wherever it is made.
			wantReply := tt.asReply && !strings.Contains(tt.comment.Body, "@kritika")
			if f.fingerprint != tt.wantFingerprint || f.asReply != wantReply {
				t.Errorf("fingerprint = %q, reply %v; want %q, %v", f.fingerprint, f.asReply, tt.wantFingerprint, wantReply)
			}
		})
	}
}

// TestBotReplies: the bot's replies in a thread are its comments that carry
// a follow-up marker, not the finding's own comment, counted over the
// whole thread when the prompt keeps only its last messages.
func TestBotReplies(t *testing.T) {
	at := func(s int64) time.Time { return time.Unix(s, 0) }
	const root = int64(1)
	inline := []forge.Comment{
		{ID: root, Author: "kritika[bot]", Body: review.FindingMarker("fp1") + "\n**Unchecked error**", CreatedAt: at(1), Inline: true},
		{ID: 2, Author: "onedr0p", Body: "the caller holds it", CreatedAt: at(2), Inline: true, InReplyTo: root},
		{ID: 3, Author: "Kritika[bot]", Body: review.FollowUpMarker(2) + "\nIt does not: see main.go:4.", CreatedAt: at(3), Inline: true, InReplyTo: root},
		{ID: 4, Author: "onedr0p", Body: "it does, in the test", CreatedAt: at(4), Inline: true, InReplyTo: root},
		{ID: 5, Author: "kritika[bot]", Body: review.FollowUpMarker(4) + "\nThe test is not the caller.", CreatedAt: at(5), Inline: true, InReplyTo: root},
		{ID: 6, Author: "kritika[bot]", Body: review.FollowUpMarker(4) + "\nAnother thread's reply.", CreatedAt: at(6), Inline: true, InReplyTo: 99},
	}
	for id := int64(7); id < 7+threadMessages+5; id++ {
		inline = append(inline, forge.Comment{ID: id, Author: "onedr0p", Body: "more", CreatedAt: at(id), Inline: true, InReplyTo: root})
	}
	asking := inline[len(inline)-1]
	f := &followUp{client: &threadForge{inline: inline}, owner: "o", repo: "r", pr: &pullRequest{number: 7}, comment: asking,
		botLogin: "kritika[bot]"}
	msgs, replied, err := f.thread(t.Context())
	if err != nil || replied != 2 || len(msgs) != threadMessages || msgs[0].Body == inline[0].Body {
		t.Fatalf("thread = %d messages, %d replies, %v; want %d messages past the root and 2 replies", len(msgs), replied, err, threadMessages)
	}
}

func TestFollowUpDiffBase(t *testing.T) {
	tests := []struct {
		name                        string
		head, mergeBase, reviewBase string
		want                        string
	}{
		{name: "an open pull request diffs against its merge base", head: "h", mergeBase: "b", reviewBase: "r", want: "b"},
		{name: "a merged one against what its last review did", head: "h", mergeBase: "h", reviewBase: "r", want: "r"},
		{name: "a merged one never reviewed has nothing else", head: "h", mergeBase: "h", want: "h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &followUp{pr: &pullRequest{headSHA: tt.head}, mergeBase: tt.mergeBase}
			if got := f.diffBase(reviewRecord{mergeBase: tt.reviewBase}); got != tt.want {
				t.Fatalf("diffBase = %q, want %q", got, tt.want)
			}
		})
	}
}

// reactForge records the reactions the bot leaves on comments and takes
// off.
type reactForge struct {
	forge.Client
	reactErr error
	reacted  []string
	removed  []int64
}

func (f *reactForge) React(_ context.Context, _, _ string, to forge.Comment, content string) (int64, error) {
	if f.reactErr != nil {
		return 0, f.reactErr
	}
	f.reacted = append(f.reacted, content)
	return to.ID + 1, nil
}

func (f *reactForge) Unreact(_ context.Context, _, _ string, _ forge.Comment, id int64) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *reactForge) Reaction(context.Context, string, string, forge.Comment, string) (int64, error) {
	return 0, nil
}

// TestFollowUpUnmark: a retried attempt that finds the reply up ends the
// marks a killed one left, as answered.
func TestFollowUpUnmark(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	for attempt, want := range map[int][]string{1: nil, 2: {forge.ReactionEyes, forge.ReactionDone}} {
		client := &reactForge{}
		f := &followUp{client: client, comment: forge.Comment{ID: 7}, logger: logger, attempt: attempt}
		f.unmark(t.Context())
		if !slices.Equal(client.reacted, want) || (attempt > 1) != slices.Equal(client.removed, []int64{8}) {
			t.Fatalf("attempt %d: reacted %v, removed %v", attempt, client.reacted, client.removed)
		}
	}
}

// TestFollowUpFailure: a failed answer marks the mention only once no
// attempt follows it.
func TestFollowUpFailure(t *testing.T) {
	boom := errors.New("worker: runner did not start")
	tests := []struct {
		name    string
		err     error
		attempt int
		want    outcome
	}{
		{"retried", boom, 1, unanswered},
		{"the last attempt", boom, 3, failed},
		{"final on the first attempt", finalError{boom}, 1, failed},
		{"a rate limit on the last attempt is snoozed", rateLimited(time.Minute), 3, unanswered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &followUp{attempt: tt.attempt, maxAttempts: 3}
			if got := f.failure(tt.err); got != tt.want {
				t.Fatalf("failure = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRequestOutcome: a review request's marks say what became of the
// review that ended last since it was made, and a failure when none did.
func TestRequestOutcome(t *testing.T) {
	tests := []struct {
		name   string
		status store.ReviewStatus
		want   outcome
	}{
		{"posted", store.ReviewCompleted, answered},
		{"failed", store.ReviewFailed, failed},
		{"none ended", "", failed},
		{"skipped", store.ReviewSkipped, unanswered},
		{"superseded", store.ReviewSuperseded, unanswered},
		{"capped", store.ReviewCapped, unanswered},
		{"canceled", store.ReviewCanceled, unanswered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestOutcome(tt.status); got != tt.want {
				t.Fatalf("requestOutcome(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}
