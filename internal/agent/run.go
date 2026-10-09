package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/textcut"
)

// Source bounds: how many URLs one run records, and how long each may be.
const (
	maxSources     = 50
	maxSourceBytes = 2048
)

// sourceEscaper percent-encodes what would end a Markdown autolink or a
// code span early, so a recorded URL renders as one link and nothing else.
var sourceEscaper = strings.NewReplacer(" ", "%20", "<", "%3C", ">", "%3E", "`", "%60")

// RunConfig configures the run tool.
type RunConfig struct {
	// Dir is the checkout the commands run in.
	Dir string
	// Env is the commands' whole environment.
	Env []string
	// CommandEnv adds variables to one command's environment, by name.
	CommandEnv map[string][]string
	// Commands map each name the model may run to the binary it runs.
	Commands map[string]string
	// Timeout bounds one command.
	Timeout time.Duration
	// MaxOutputBytes caps the combined output kept of one command.
	MaxOutputBytes int
	// Proxied says the commands reach the network through the egress
	// gateway, which fetches a plain http:// URL over HTTPS.
	Proxied bool
	// Note, when set, is appended to the tool's description.
	Note string
	// Mask, when set, is applied to a command's output before the model
	// sees it, to keep a credential a command printed out of the
	// conversation.
	Mask func(string) string
	// Keep, when set, says where a cut output is kept whole.
	Keep *Kept
}

// Kept says where the run tool keeps a cut output whole, masked, for the
// commands and read_file to read in place of the cut.
type Kept struct {
	// Dir is the directory the files are written to, and Rel the same
	// directory as the commands see it from the checkout.
	Dir, Rel string
	// FileBytes caps one output: a longer one is not kept.
	FileBytes int
	// Budget bounds what is written to Dir in all.
	Budget *WriteBudget
}

// WriteBudget is what the tools may write beside the checkout between
// them, in bytes: fetch_repo its fetched files and diffs, the run tool its
// kept outputs. The loop calls tools one at a time, so it is not locked.
type WriteBudget struct {
	left int64
}

// NewWriteBudget is a budget of n bytes.
func NewWriteBudget(n int64) *WriteBudget { return &WriteBudget{left: n} }

// Left is what may still be written.
func (b *WriteBudget) Left() int64 { return b.left }

// Spend counts n bytes written.
func (b *WriteBudget) Spend(n int64) { b.left -= n }

// RunTool executes one allowlisted binary with the model's arguments,
// directly and without a shell, in a checkout of the head commit, and
// records as a source the review consulted every http(s) URL curl is given
// and what each gh call reads, and which commands it ran.
type RunTool struct {
	cfg     RunConfig
	names   []string
	schema  json.RawMessage
	sources []string
	ran     []string
	// calls counts the commands run, which number the kept outputs.
	calls int
}

// NewRunTool builds the run tool over c.Commands.
func NewRunTool(c RunConfig) *RunTool {
	names := slices.Sorted(maps.Keys(c.Commands))
	// A []string always encodes.
	enum, _ := json.Marshal(names)
	schema := fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "enum": %s, "description": "The command to run."},
		"args": {"type": "array", "items": {"type": "string"}, "description": "Arguments, one per element, passed exactly as given."}
	},
	"required": ["command"],
	"additionalProperties": false
}`, enum)
	return &RunTool{cfg: c, names: names, schema: json.RawMessage(schema)}
}

// Names are the commands the tool offers, sorted.
func (rt *RunTool) Names() []string { return rt.names }

// Sources are the URLs curl was given and gh read, in first-use order,
// never nil.
func (rt *RunTool) Sources() []string { return append([]string{}, rt.sources...) }

// Ran are the commands the model ran, each once in first-use order, never
// nil.
func (rt *RunTool) Ran() []string { return append([]string{}, rt.ran...) }

// Kept is where the tool keeps a cut output whole, nil when it keeps none.
func (rt *RunTool) Kept() *Kept { return rt.cfg.Keep }

func (rt *RunTool) Def() model.ToolDef {
	desc := fmt.Sprintf("Run one of these commands in a checkout of the head commit: %s. The command runs directly, "+
		"without a shell: arguments are passed exactly as given, with no globbing, pipes or redirection. It is stopped "+
		"after %s, and its exit code and combined output are returned.", strings.Join(rt.names, ", "), rt.cfg.Timeout)
	if slices.Contains(rt.names, "rg") {
		desc += " rg searches file contents where the grep tool falls short: with context lines (`rg -C 3 <pattern>`), " +
			"by file type (`rg -t go <pattern>`) or across lines (`rg -U <pattern>`)."
	}
	if slices.Contains(rt.names, "fd") {
		desc += " fd finds files and directories by name, extension or type (`fd -e yaml values`, `fd -t d charts`), " +
			"where the list_files tool's glob is not enough."
	}
	if slices.Contains(rt.names, "jq") {
		desc += " jq reads one path of a JSON file (`jq '.dependencies' package.json`), where read_file would return " +
			"the whole file."
	}
	if slices.Contains(rt.names, "yq") {
		desc += " yq does the same for a YAML file (`yq '.spec.values.image' helmrelease.yaml`), and reads JSON too."
	}
	if slices.Contains(rt.names, "gh") {
		desc += " Use gh, not curl, for anything on GitHub: it is signed in to read public repositories, such as " +
			"`gh release view <tag> -R <owner>/<repo>` for a release's notes or `gh pr view <number> -R <owner>/<repo>` " +
			"for an upstream pull request."
	}
	if slices.Contains(rt.names, "curl") {
		if rt.cfg.Proxied {
			desc += " The network is reached through a gateway that allows only some hosts. Give curl http:// URLs: " +
				"the gateway fetches them over HTTPS and authenticates to the hosts it holds a credential for."
		} else {
			desc += " Give curl https:// URLs."
		}
	}
	if rt.cfg.Keep != nil {
		desc += fmt.Sprintf(" An output cut to %d KiB is kept whole in a file its note names, under %s/: search that file with "+
			"the commands, or read it with read_file in line ranges, rather than run the command again with narrower arguments.",
			rt.cfg.MaxOutputBytes>>10, rt.cfg.Keep.Rel)
	}
	if rt.cfg.Note != "" {
		desc += " " + rt.cfg.Note
	}
	return model.ToolDef{Name: "run", Description: desc, InputSchema: rt.schema}
}

func (rt *RunTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := decodeInput(input, &req); err != nil {
		return "", fmt.Errorf("agent: run: %w", err)
	}
	bin, ok := rt.cfg.Commands[req.Command]
	if !ok {
		return "", fmt.Errorf("agent: run: %q is not one of %s", req.Command, strings.Join(rt.names, ", "))
	}
	if why := refusedArgs(req.Command, req.Args); why != "" {
		return "", fmt.Errorf("agent: run: %s: %s", req.Command, why)
	}
	if !slices.Contains(rt.ran, req.Command) {
		rt.ran = append(rt.ran, req.Command)
	}

	rt.calls++
	cctx, cancel := context.WithTimeout(ctx, rt.cfg.Timeout)
	defer cancel()
	out := &cappedBuffer{max: rt.cfg.MaxOutputBytes, keep: rt.cfg.MaxOutputBytes}
	if k := rt.cfg.Keep; k != nil {
		// An output that would not be kept is not buffered past the cut.
		out.keep = max(out.max, int(min(int64(k.FileBytes), k.Budget.Left())))
	}
	errs := &stderrTail{out: out, max: rt.cfg.MaxOutputBytes / stderrShare, last: '\n'}
	cmd := exec.CommandContext(cctx, bin, req.Args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = rt.cfg.Dir, append(slices.Clone(rt.cfg.Env), rt.cfg.CommandEnv[req.Command]...), out, errs
	// Once the command is killed, a child still holding its output open is
	// not waited for.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("agent: run: %w", ctx.Err())
	case cctx.Err() != nil:
		stopped := fmt.Sprintf("stopped after %s\n%s", rt.cfg.Timeout, rt.mask(out.String()))
		return rt.result(stopped, out, out.dropped(), rt.mask(errs.String())), nil
	case err != nil:
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			return "", fmt.Errorf("agent: run: %s: %w", req.Command, err)
		}
	}
	code := cmd.ProcessState.ExitCode()
	if code == 0 {
		rt.recordRead(req.Command, req.Args)
	}
	text, dropped, stderr := rt.mask(out.String()), out.dropped(), rt.mask(errs.String())
	if req.Command == "gh" && code != 0 {
		if hint := ghUnknownCommand(text); hint != "" {
			text, dropped, stderr = hint, 0, ""
		}
	}
	return rt.result(fmt.Sprintf("exit code %d\n%s", code, text), out, dropped, stderr), nil
}

// A cut output keeps at most MaxOutputBytes/stderrShare of the end of the
// command's stderr, after stderrLead.
const (
	stderrShare = 8
	stderrLead  = "\n[the end of stderr, from the part cut above:]\n"
)

// result is a command's result cut to MaxOutputBytes with its note, which
// counts the bytes the buffer dropped as well, so the loop's own cut to the
// same limit leaves it whole. A cut output is then kept whole in a file,
// which a note after the cut's names, from out. stderr is the end of the
// stderr the buffer dropped, where an error printed after a long output
// lands: it follows the notes, the rest cut shorter to make room, unless
// it would take more than a quarter of the limit.
func (rt *RunTool) result(s string, out *cappedBuffer, dropped int, stderr string) string {
	var tail string
	// An output that fits the buffer is still cut when its first line
	// takes it past the limit.
	if (dropped > 0 || len(s) > rt.cfg.MaxOutputBytes) && rt.cfg.Keep != nil {
		tail = rt.keep(out)
	}
	if end := stderrLead + stderr; stderr != "" && len(end) <= rt.cfg.MaxOutputBytes/4 {
		tail += end
	}
	if tail == "" {
		return textcut.Cut(s, rt.cfg.MaxOutputBytes, dropped)
	}
	return textcut.Cut(s, rt.cfg.MaxOutputBytes-len(tail), dropped) + tail
}

// keep writes the whole output in out, masked, to the file for this call
// and returns the note naming it, or saying why it is not kept: the output
// is over what one file may hold, or past what the review may write.
func (rt *RunTool) keep(out *cappedBuffer) string {
	k := rt.cfg.Keep
	whole, ok := out.whole()
	switch {
	case out.total > k.FileBytes:
		return fmt.Sprintf("\n[the whole output is not kept: %d bytes, over the %d one file may hold]", out.total, k.FileBytes)
	case !ok:
		return fmt.Sprintf("\n[the whole output is not kept: %d bytes, past what this review may write]", out.total)
	}
	masked := rt.mask(string(whole))
	name := fmt.Sprintf("run-%d.out", rt.calls)
	if err := os.WriteFile(filepath.Join(k.Dir, name), []byte(masked), 0o644); err != nil {
		return fmt.Sprintf("\n[the whole output is not kept: %s]", err)
	}
	k.Budget.Spend(int64(len(masked)))
	return fmt.Sprintf("\n[the whole output is in %s/%s]", k.Rel, name)
}

func (rt *RunTool) mask(out string) string {
	if rt.cfg.Mask == nil {
		return out
	}
	return rt.cfg.Mask(out)
}

// ghRefused are the gh commands that print the token gh is signed in with
// or run a program of the caller's choosing.
var ghRefused = []string{"auth", "alias", "config", "extension", "extensions", "ext"}

// ghFlagTakesValue reports whether a flag before gh's command takes the
// next argument for its value: every one but the root command's own,
// which take none, as cobra parses them. A flag with its value attached
// by "=" and a group of short flags take nothing more.
func ghFlagTakesValue(a string) bool {
	switch {
	case a == "-h" || a == "--help" || a == "--version" || a == "--" || strings.Contains(a, "="):
		return false
	case strings.HasPrefix(a, "--"):
		return true
	}
	return len(a) == 2
}

// fdValueFlags are fd's short flags that take a value, which ends a group
// of short flags: what follows one in the same argument is its value.
const fdValueFlags = "dEteSocjC"

// refusedArgs says why command must not run with args, or "": the
// commands are an allowlist, and these arguments would have one run a
// program outside it or print a credential. The model's arguments are
// steered by the content under review, so they are not trusted.
func refusedArgs(command string, args []string) string {
	switch command {
	case "gh":
		// gh's command is its first positional argument, with the flags
		// before it read as cobra reads them: a flag the root command does
		// not have takes the next argument for its value, so "gh
		// --hostname h auth token" runs "auth token".
		for i := 0; i < len(args); i++ {
			a := args[i]
			if !strings.HasPrefix(a, "-") {
				if slices.Contains(ghRefused, a) {
					return "gh " + a + " is not offered"
				}
				break
			}
			if ghFlagTakesValue(a) {
				i++
			}
		}
	case "rg":
		for _, a := range args {
			name, _, _ := strings.Cut(a, "=")
			switch {
			case a == "--":
				return ""
			case name == "--pre" || name == "--hostname-bin":
				return name + " is not offered: it runs another program"
			}
		}
	case "fd":
		for _, a := range args {
			name, _, _ := strings.Cut(a, "=")
			switch {
			case a == "--":
				return ""
			case name == "--exec" || name == "--exec-batch":
				return name + " is not offered: it runs another program"
			case strings.HasPrefix(a, "--") || !strings.HasPrefix(a, "-"):
				continue
			}
			for _, f := range a[1:] {
				if f == 'x' || f == 'X' {
					return "-" + string(f) + " is not offered: it runs another program"
				}
				if strings.ContainsRune(fdValueFlags, f) {
					break
				}
			}
		}
	}
	return ""
}

// recordRead keeps as sources what a command that succeeded read: the
// URLs curl was given and the resource a gh call names. One that failed
// read nothing a review rests on.
func (rt *RunTool) recordRead(command string, args []string) {
	switch command {
	case "curl":
		rt.record(args)
	case "gh":
		if s := ghSource(args); s != "" {
			rt.record([]string{s})
		}
	}
}

// record keeps each http(s) URL in args as a source, without any
// credentials it carries.
func (rt *RunTool) record(args []string) {
	for _, a := range args {
		u, err := url.Parse(a)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		u.User = nil
		if rt.cfg.Proxied {
			u.Scheme = "https"
		}
		s := sourceEscaper.Replace(u.String())
		if len(rt.sources) < maxSources && len(s) <= maxSourceBytes && !slices.Contains(rt.sources, s) {
			rt.sources = append(rt.sources, s)
		}
	}
}

// cappedBuffer keeps the first keep bytes written to it and counts the
// rest, so a command's output never grows past what the tool keeps: the
// first max of them are what the tool returns, the rest go to the file a
// cut output is kept whole in. A command's stdout and stderr write to it
// from their own goroutines.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// keep is at least max.
	max, keep int
	total     int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.write(p)
	return len(p), nil
}

// write keeps what of p fits and returns how many of its bytes, the last
// ones, the result drops.
func (b *cappedBuffer) write(p []byte) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	shown := min(max(b.max-b.total, 0), len(p))
	kept := min(max(b.keep-b.total, 0), len(p))
	b.buf.Write(p[:kept])
	b.total += len(p)
	return len(p) - shown
}

// String is the output the result returns as valid UTF-8; dropped counts
// the rest.
func (b *cappedBuffer) String() string {
	return strings.ToValidUTF8(string(b.buf.Bytes()[:min(b.buf.Len(), b.max)]), "�")
}

func (b *cappedBuffer) dropped() int { return max(b.total-b.max, 0) }

// whole is the whole output, false when it ran past keep.
func (b *cappedBuffer) whole() ([]byte, bool) {
	if b.total > b.keep {
		return nil, false
	}
	return b.buf.Bytes(), true
}

// stderrTail is a command's stderr: written to out with its stdout, and
// the last max bytes out dropped of it kept besides. Those are always the
// end of the stream, since out drops everything once it is full.
type stderrTail struct {
	out  *cappedBuffer
	max  int
	tail []byte
	// before is the stderr byte just before the tail, and last the last
	// one written; '\n' stands for the start of the stream.
	before, last byte
	started      bool
}

func (s *stderrTail) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if n := s.out.write(p); n > 0 {
		if !s.started {
			s.started, s.before = true, s.last
			if n < len(p) {
				s.before = p[len(p)-n-1]
			}
		}
		s.tail = append(s.tail, p[len(p)-n:]...)
		if over := len(s.tail) - s.max; over > 0 {
			s.before = s.tail[over-1]
			s.tail = s.tail[:copy(s.tail, s.tail[over:])]
		}
	}
	s.last = p[len(p)-1]
	return len(p), nil
}

// String is the kept end of stderr from its first whole line, as valid
// UTF-8. A line cut at its start is left out: the mask applied to it
// recognises only whole credentials, and the start of one cut there would
// leave the rest of it unmasked.
func (s *stderrTail) String() string {
	t := s.tail
	if s.before != '\n' {
		i := bytes.IndexByte(t, '\n')
		if i < 0 {
			return ""
		}
		t = t[i+1:]
	}
	return strings.ToValidUTF8(string(t), "�")
}
