package worker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/home-operations/kritika/internal/forge"
	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/home-operations/kritika/internal/review"
	"github.com/home-operations/kritika/internal/runner"
	"github.com/home-operations/kritika/internal/store"
)

func TestSplitDropped(t *testing.T) {
	loose := review.Finding{Path: "a.go", Line: 3, Title: "off the diff"}
	other := review.Finding{Path: "b.go", Line: 9, Title: "also off"}
	drop := func(reason review.DropReason, f review.Finding) review.Dropped {
		return review.Dropped{Finding: f, Reason: reason}
	}
	tests := []struct {
		name           string
		dropped        []review.Dropped
		wantUnanchored []review.Finding
		wantNotes      []string
	}{
		{name: "nothing dropped"},
		{name: "an unanchored finding is returned without a note",
			dropped: []review.Dropped{drop(review.DropUnanchored, loose)}, wantUnanchored: []review.Finding{loose}},
		{name: "other reasons are counted in one sorted note",
			dropped:   []review.Dropped{drop(review.DropNoFix, loose), drop(review.DropIncomplete, other), drop(review.DropIncomplete, loose)},
			wantNotes: []string{"3 finding(s) were dropped (incomplete: 2, no_suggested_fix: 1)"}},
		{name: "unanchored and counted reasons both come back",
			dropped:        []review.Dropped{drop(review.DropBadSeverity, other), drop(review.DropUnanchored, loose)},
			wantUnanchored: []review.Finding{loose}, wantNotes: []string{"1 finding(s) were dropped (bad_severity: 1)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unanchored, notes := splitDropped(tt.dropped)
			if !reflect.DeepEqual(unanchored, tt.wantUnanchored) {
				t.Errorf("unanchored = %+v, want %+v", unanchored, tt.wantUnanchored)
			}
			if !slices.Equal(notes, tt.wantNotes) {
				t.Errorf("notes = %q, want %q", notes, tt.wantNotes)
			}
		})
	}
}

func TestMarkedInline(t *testing.T) {
	const login = "kritika[bot]"
	fp := review.Fingerprint(review.Finding{Path: "a.go", Title: "nil deref"})
	marked := func(id int64, author, fingerprint string, inReplyTo int64) forge.Comment {
		return forge.Comment{ID: id, Author: author, Body: "**nil deref**\n\n" + review.FindingMarker(fingerprint), InReplyTo: inReplyTo}
	}
	outdated := func(c forge.Comment) forge.Comment {
		c.Outdated = true
		return c
	}
	tests := []struct {
		name     string
		comments []forge.Comment
		want     map[string]int64
	}{
		{name: "the bot's root comment maps its fingerprint", comments: []forge.Comment{marked(5, login, fp, 0)}, want: map[string]int64{fp: 5}},
		{name: "a reply is ignored", comments: []forge.Comment{marked(6, login, fp, 5)}, want: map[string]int64{}},
		{name: "another author's planted marker is ignored", comments: []forge.Comment{marked(7, "mallory", fp, 0)}, want: map[string]int64{}},
		{name: "the author match ignores case", comments: []forge.Comment{marked(8, "Kritika[Bot]", fp, 0)}, want: map[string]int64{fp: 8}},
		{name: "a later comment for the same finding wins",
			comments: []forge.Comment{marked(9, login, fp, 0), marked(10, login, fp, 0)}, want: map[string]int64{fp: 10}},
		{name: "a comment without a marker contributes nothing",
			comments: []forge.Comment{{ID: 11, Author: login, Body: "looks fine"}}, want: map[string]int64{}},
		{name: "an outdated comment is passed over",
			comments: []forge.Comment{outdated(marked(12, login, fp, 0))}, want: map[string]int64{}},
		{name: "a current comment stands over a later outdated one",
			comments: []forge.Comment{marked(13, login, fp, 0), outdated(marked(14, login, fp, 0))}, want: map[string]int64{fp: 13}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := markedInline(tt.comments, login); !maps.Equal(got, tt.want) {
				t.Errorf("markedInline = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkipDescription(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		filter string
		want   string
	}{
		{name: "the runner's unchanged patch", reason: runner.SkipUnchangedPatch, want: "patch unchanged since the last review"},
		{name: "filtered", reason: string(repoconfig.SkipFiltered), want: "filtered"},
		{name: "filtered by a named condition", reason: string(repoconfig.SkipFiltered), filter: "too-large", want: "filtered: too-large"},
		{name: "a repository skip reason", reason: string(repoconfig.SkipOnlyPaths), want: repoconfig.SkipOnlyPaths.Description()},
		{name: "disabled", reason: string(repoconfig.SkipDisabled), want: "disabled in " + repoconfig.FileName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipDescription(tt.reason, tt.filter); got != tt.want {
				t.Errorf("skipDescription(%q, %q) = %q, want %q", tt.reason, tt.filter, got, tt.want)
			}
		})
	}
}

func TestSkillsNote(t *testing.T) {
	tests := []struct {
		name            string
		offered, opened []string
		want            string
	}{
		{name: "none offered"},
		{name: "none offered, as the runner writes it", offered: []string{}, opened: []string{}},
		{name: "offered and none read", offered: []string{"review-renovate-pr", "go-style"}, want: "Skills offered: review-renovate-pr, go-style; none read"},
		{
			name: "offered and one read", offered: []string{"review-renovate-pr", "go-style"}, opened: []string{"review-renovate-pr"},
			want: "Skills offered: review-renovate-pr, go-style; read: review-renovate-pr",
		},
		{
			name: "each read, in the order read", offered: []string{"review-renovate-pr", "go-style"}, opened: []string{"go-style", "review-renovate-pr"},
			want: "Skills offered: review-renovate-pr, go-style; read: go-style, review-renovate-pr",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skillsNote(tt.offered, tt.opened); got != tt.want {
				t.Errorf("skillsNote(%q, %q) = %q, want %q", tt.offered, tt.opened, got, tt.want)
			}
		})
	}
}

func TestCommandsNote(t *testing.T) {
	tests := []struct {
		name         string
		offered, ran []string
		want         string
	}{
		{name: "none offered"},
		{name: "none offered, as the runner writes it", offered: []string{}, ran: []string{}},
		{name: "offered and none run", offered: []string{"gh", "helm"}, want: "Commands offered: gh, helm; none run"},
		{
			name: "each run, in the order first run", offered: []string{"gh", "helm", "kubectl"}, ran: []string{"helm", "gh"},
			want: "Commands offered: gh, helm, kubectl; run: helm, gh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandsNote(tt.offered, tt.ran); got != tt.want {
				t.Errorf("commandsNote(%q, %q) = %q, want %q", tt.offered, tt.ran, got, tt.want)
			}
		})
	}
}

func TestResolvedThreads(t *testing.T) {
	prior := []priorFinding{
		{Path: "a.go", Title: "Unchecked error", commentID: 11},
		{Path: "b.go", Title: "Stale comment", commentID: 12},
		{Path: "c.go", Title: "Never posted inline"},
	}
	tests := []struct {
		name     string
		reported []review.Finding
		want     []int64
	}{
		{name: "every finding gone resolves every posted thread", want: []int64{11, 12}},
		{name: "a finding reported again keeps its thread", reported: []review.Finding{{Path: "a.go", Title: "unchecked  ERROR"}}, want: []int64{12}},
		{name: "a different finding on the same path resolves nothing of it", reported: []review.Finding{{Path: "a.go", Title: "Other"}, {Path: "b.go", Title: "Stale comment"}}, want: []int64{11}},
		{
			name:     "a finding reported again in other words, carrying the prior's fingerprint, keeps its thread",
			reported: []review.Finding{{Path: "a.go", Title: "Error is dropped", Fingerprint: review.Fingerprint(review.Finding{Path: "a.go", Title: "Unchecked error"})}},
			want:     []int64{12},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvedThreads(tt.reported, prior)
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolvedThreads() = %v, want %v", got, tt.want)
			}
		})
	}
}

// linkForge links files and threads as a forge would, for the summary's
// earlier findings.
type linkForge struct{ forge.Client }

func (linkForge) FileURL(_, _, sha, path string, line, _ int) string {
	return fmt.Sprintf("https://f/%s/%s#L%d", sha, path, line)
}

func (linkForge) ThreadURL(_, _ string, _ int, id int64) string {
	return fmt.Sprintf("https://f/t/%d", id)
}

// TestPriorFindings: the summary's earlier findings are the last review's
// this one did not report again, on the diff or off it, linked to their
// threads, then the dismissed ones.
func TestPriorFindings(t *testing.T) {
	p := &publishPhase{
		client: linkForge{}, pr: &pullRequest{repository: "o/r", number: 1},
		prior: priorReview{
			headSHA: "h1",
			findings: []priorFinding{
				{Path: "a.go", Line: 11, Severity: review.SeverityImportant, Title: "Nil map write", commentID: 42},
				{Path: "b.go", Line: 3, Severity: review.SeverityNit, Title: "Typo", commentID: 43},
				{Path: "c.go", Line: 5, Severity: review.SeverityNit, Title: "Never posted"},
			},
			dismissed: []store.Dismissal{{Finding: review.Finding{Path: "d.go", Line: 8, Title: "Dismissed"}, Reason: "intended", CommentID: 44}},
		},
	}
	// Nil map write is reported again outside the diff, under the prior
	// finding's fingerprint; Typo is not reported again.
	reported := []review.Finding{{Path: "a.go", Line: 40, Severity: review.SeverityImportant, Title: "Assignment panics",
		Fingerprint: review.Fingerprint(review.Finding{Path: "a.go", Title: "Nil map write"})}}
	got := p.priorFindings(reported)
	if len(got) != 3 || got[0].Title != "Typo" || !got[0].Resolved || got[0].ThreadURL != "https://f/t/43" || got[0].URL != "https://f/h1/b.go#L3" ||
		got[1].Title != "Never posted" || got[1].ThreadURL != "" || !got[2].Dismissed || got[2].DismissReason != "intended" || got[2].ThreadURL != "https://f/t/44" {
		t.Fatalf("priorFindings() = %+v", got)
	}
	if ids := resolvedThreads(reported, p.prior.findings); !slices.Equal(ids, []int64{43}) {
		t.Fatalf("resolvedThreads() = %v, want Typo's thread alone", ids)
	}
}

// stickyForge keeps a pull request's conversation comments by id and
// records the calls a sticky comment write makes.
type stickyForge struct {
	forge.Client
	comments map[int64]string
	// updateErr fails every edit, over the missing-comment error.
	updateErr error
	calls     []string
}

func (f *stickyForge) BotLogin(context.Context) (string, error) { return "kritika[bot]", nil }

func (f *stickyForge) FindComment(_ context.Context, _, _ string, _ int, _, marker string) (int64, error) {
	f.calls = append(f.calls, "find")
	for _, id := range slices.Sorted(maps.Keys(f.comments)) {
		if f.comments[id] == marker {
			return id, nil
		}
	}
	return 0, nil
}

func (f *stickyForge) CreateComment(_ context.Context, _, _ string, _ int, body string) (int64, error) {
	f.calls = append(f.calls, "create")
	f.comments[200] = body
	return 200, nil
}

func (f *stickyForge) UpdateComment(_ context.Context, _, _ string, id int64, body string) error {
	f.calls = append(f.calls, fmt.Sprintf("update %d", id))
	if f.updateErr != nil {
		return f.updateErr
	}
	if _, ok := f.comments[id]; !ok {
		return fmt.Errorf("fake: comment %d: %w", id, fs.ErrNotExist)
	}
	f.comments[id] = body
	return nil
}

func TestWriteSticky(t *testing.T) {
	t.Parallel()
	const marker = "<!-- kritika:pr-7 -->"
	tests := []struct {
		name      string
		stored    int64
		comments  map[int64]string
		updateErr error
		want      int64
		wantErr   error
		calls     []string
	}{
		{name: "the stored comment is edited", stored: 100, comments: map[int64]string{100: marker},
			want: 100, calls: []string{"update 100"}},
		{name: "a stored comment the forge no longer has is replaced", stored: 100, comments: map[int64]string{},
			want: 200, calls: []string{"update 100", "find", "create"}},
		{name: "a stored comment the forge no longer has yields to one by its marker", stored: 100, comments: map[int64]string{150: marker},
			want: 150, calls: []string{"update 100", "find", "update 150"}},
		{name: "another error editing the stored comment fails", stored: 100, comments: map[int64]string{100: marker},
			updateErr: errForge, wantErr: errForge, calls: []string{"update 100"}},
		{name: "nothing stored finds the comment by its marker", comments: map[int64]string{150: marker},
			want: 150, calls: []string{"find", "update 150"}},
		{name: "nothing stored and none on the forge creates one", comments: map[int64]string{},
			want: 200, calls: []string{"find", "create"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &stickyForge{comments: tt.comments, updateErr: tt.updateErr}
			p := &publishPhase{client: f, pr: &pullRequest{repository: "o/r", number: 7}, logger: slog.New(slog.DiscardHandler)}
			got, err := p.writeSticky(t.Context(), tt.stored, "body")
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("writeSticky(%d) = %d, %v; want %d, %v", tt.stored, got, err, tt.want, tt.wantErr)
			}
			if !slices.Equal(f.calls, tt.calls) {
				t.Errorf("forge calls = %v, want %v", f.calls, tt.calls)
			}
			if err == nil && f.comments[got] != "body" {
				t.Errorf("comment %d = %q, want the body written", got, f.comments[got])
			}
		})
	}
}

var errForge = errors.New("forge: forbidden")

// TestUnfinishedParts: each part of a split review that ended before it
// submitted is named with its files, the first few of them, and none is
// named for a review whose parts all submitted or one not split.
func TestUnfinishedParts(t *testing.T) {
	parts := []store.AgentPart{
		{Paths: []string{"a/x.go"}, Stop: "submitted"},
		{Paths: []string{"b/1.go", "b/2.go", "b/3.go", "b/4.go", "b/5.go", "b/6.go", "b/7.go"}, Stop: "no_submit"},
		{Paths: []string{"c/z.go"}, Stop: "canceled"},
	}
	got := unfinishedParts(parts)
	want := []string{
		"Part 2 of 3 ended before it submitted, so its files went unreviewed: b/1.go, b/2.go, b/3.go, b/4.go, b/5.go, 2 more",
		"Part 3 of 3 ended before it submitted, so its files went unreviewed: c/z.go",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("notes = %q, want %q", got, want)
	}
	if got := unfinishedParts(parts[:1]); got != nil {
		t.Fatalf("a submitted part = %q, want no note", got)
	}
	if got := unfinishedParts(nil); got != nil {
		t.Fatalf("a review not split = %q, want no note", got)
	}
}

// TestRecheckedBy: a split review checks the last review's findings on a
// path again when the part that had them submitted, the first part having
// those on files no part reviews; a review not split checks them all.
func TestRecheckedBy(t *testing.T) {
	parts := []store.AgentPart{
		{Paths: []string{"a/x.go"}, Stop: "submitted"},
		{Paths: []string{"b/y.go"}, Stop: "no_submit"},
	}
	checked := recheckedBy(parts)
	for path, want := range map[string]bool{"a/x.go": true, "b/y.go": false, "docs/unchanged.md": true} {
		if got := checked(path); got != want {
			t.Fatalf("rechecked(%q) = %v, want %v", path, got, want)
		}
	}
	parts[0].Stop = "canceled"
	if recheckedBy(parts)("docs/unchanged.md") {
		t.Fatal("a file no part reviews counts as checked though the first part never submitted")
	}
	if !recheckedBy(nil)("b/y.go") {
		t.Fatal("a review not split checks every finding again")
	}
}
