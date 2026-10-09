package review

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/home-operations/kritika/internal/textcut"
)

// MaxRenderBytes bounds a rendered comment, marker included. GitHub rejects
// comment bodies over 65,536 characters, and a character is at least a byte.
const MaxRenderBytes = 64 << 10

// renderTimeout bounds one render, parse included.
const renderTimeout = 2 * time.Second

//go:embed templates/summary.md.tmpl
var defaultSummary string

//go:embed templates/inline.md.tmpl
var defaultInline string

// Templates are repository-supplied template sources. An empty field means
// the embedded default.
type Templates struct {
	Summary, Inline string
}

// RenderData is what the summary template sees as its dot; the inline
// template's dot is one Finding.
type RenderData struct {
	Number  int
	HeadSHA string
	// HeadURL links the head commit on the forge, "" when unknown.
	HeadURL string
	// HeadSubject is the head commit's subject line as FooterSubject cuts
	// and escapes it, "" when unknown.
	HeadSubject string
	// Reviews is how many reviews of the pull request this one makes.
	Reviews int
	Model   string
	// Effort is how hard the model reasoned, "" for the provider's default.
	Effort string
	// Cost is what the pull request's reviews have cost together, as
	// FormatUSD writes it, "" where the repository does not show it.
	Cost string
	// AuthorIsBot is whether a bot opened the pull request; the default
	// template then leaves out the praise, which a mechanical change
	// earns nothing by.
	AuthorIsBot  bool
	Result       Result
	Counts       Counts
	Notes        []string
	Incremental  bool
	PriorHeadSHA string
	// PriorHeadURL links the last review's head on the forge, "" when
	// unknown.
	PriorHeadURL string
	// Prior are the last review's findings this review did not report
	// again, each resolved or dismissed, when this review builds on it.
	Prior []PriorFinding
	// Unanchored are findings on lines the diff does not show, which
	// this review posts no inline comment for: listed in the summary only,
	// each linked to the thread an earlier report of it opened, if any.
	Unanchored []Finding
	// HeldBack are the findings in the diff an incremental re-review held
	// back (see HoldBack): listed only, neither posted, counted nor scored.
	HeldBack []Finding
	// Incomplete, when set, says why the head was not fully reviewed; the
	// default template then states that instead of a verdict.
	Incomplete string
	// Sources are links to what the review's commands fetched, from
	// the runner's record rather than the model's answer, as SourceLinks
	// gives them.
	Sources []string
	// Confidence is the score a second model gave the pull request, nil
	// when the repository asks for none or the scorer did not answer.
	Confidence *Confidence
	// Approval is what became of kritika's approval of the head, nil
	// where the repository does not have it approve.
	Approval *Approval
	// WebURL is the dashboard's origin, without a trailing slash, and
	// PullURL the pull request's page on it, where an admin can re-run
	// the review; both "" when the dashboard has no public URL. The
	// default template then leaves the re-run badge out.
	WebURL, PullURL string
}

// Approval is whether the reviewed head stands approved by kritika, and
// the reason, "" where the summary states it already: an approval a
// confidence score decided says nothing the score's own line does not.
type Approval struct {
	Approved bool
	Reason   string
}

// FormatUSD writes an amount in dollars to the cent, and under a cent to
// the hundredth of one so a small review does not read as free.
func FormatUSD(amount float64) string {
	switch {
	case amount == 0:
		return "$0"
	case amount < 0.01:
		return fmt.Sprintf("$%.4f", amount)
	}
	return fmt.Sprintf("$%.2f", amount)
}

// footerSubjectRunes is how much of a commit subject the footer shows.
const footerSubjectRunes = 40

// FooterSubject cuts a commit subject to one line of footerSubjectRunes,
// marking the cut with an ellipsis, and escapes what Markdown would read
// as markup, since a pull request's author writes the subject and it
// lands inside the footer's link text.
func FooterSubject(subject string) string {
	subject, _, _ = strings.Cut(subject, "\n")
	subject = strings.TrimSpace(subject)
	if runes := []rune(subject); len(runes) > footerSubjectRunes {
		subject = strings.TrimSpace(string(runes[:footerSubjectRunes])) + "..."
	}
	var b strings.Builder
	for _, r := range subject {
		if strings.ContainsRune("\\`*_[]<>", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PullPageURL is the dashboard page of pull request number of owner/repo
// under the account name on forge, for a dashboard served at web: "" when
// web is nil.
func PullPageURL(web *url.URL, forge, name, owner, repo string, number int) string {
	if web == nil {
		return ""
	}
	return web.String() + "/#/a/" + url.PathEscape(forge) + "/" + url.PathEscape(name) +
		"/pulls/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/" + strconv.Itoa(number)
}

var (
	errTooLarge = errors.New("review: rendered output exceeds the size limit")
	errSandbox  = errors.New("review: template exceeded a sandbox limit")
)

// RenderSummary renders the sticky comment. kritika's marker always leads,
// whatever the template does, so sticky discovery cannot be defeated by a
// template. When the repository's template fails, the default is used and
// the returned note, also shown in the comment, says why.
func RenderSummary(ctx context.Context, t Templates, d RenderData) (body string, notes []string) {
	marker := Marker(d.Number) + "\n"
	limit := MaxRenderBytes - len(marker)
	if t.Summary != "" {
		out, err := render(ctx, t.Summary, d, limit)
		if err == nil {
			return marker + out, nil
		}
		note := fallbackNote("summary", err)
		notes = append(notes, note)
		d.Notes = append(slices.Clone(d.Notes), note)
	}
	out, err := render(context.WithoutCancel(ctx), defaultSummary, d, limit)
	if err != nil {
		// The default renders data kritika bounds itself; failing here is a
		// bug, but the comment must still carry the marker and the take.
		out = textcut.Prefix(fmt.Sprintf("## Kritika Review\n\n%s\n", d.Result.Summary.Take), limit)
	}
	return marker + out, notes
}

// RenderInline renders one finding as an inline review comment, led by its
// FindingMarker, falling back to the default template as RenderSummary
// does.
func RenderInline(ctx context.Context, t Templates, f Finding) (string, []string) {
	marker := FindingMarker(Fingerprint(f)) + "\n"
	limit := MaxRenderBytes - len(marker)
	var notes []string
	if t.Inline != "" {
		out, err := render(ctx, t.Inline, f, limit)
		if err == nil {
			return marker + out, nil
		}
		notes = append(notes, fallbackNote("inline", err))
	}
	out, err := render(context.WithoutCancel(ctx), defaultInline, f, limit)
	if err != nil {
		out = textcut.Prefix(fmt.Sprintf("**[%s]** **%s**\n\n%s\n", f.Severity, f.Title, f.Explanation), limit)
	}
	return marker + out, notes
}

func fallbackNote(which string, err error) string {
	why := "failed to render"
	switch {
	case errors.Is(err, errTooLarge):
		why = fmt.Sprintf("produced more than %d KiB", MaxRenderBytes>>10)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		why = "ran out of time"
	}
	return fmt.Sprintf("The repository's %s template %s, so kritika's default was used", which, why)
}

// render parses and executes src in a sandbox, bounded by ctx and
// renderTimeout. A render that outlives its deadline is abandoned; the
// guards make it stop at its next loop, call or write.
func render(ctx context.Context, src string, data any, limit int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%w: panic: %v", errSandbox, r)}
			}
		}()
		out, err := execute(ctx, src, data, limit)
		ch <- result{out, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && ctx.Err() != nil {
			return "", fmt.Errorf("review: render: %w", ctx.Err())
		}
		return r.out, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("review: render: %w", ctx.Err())
	}
}
