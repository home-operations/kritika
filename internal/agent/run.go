package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/home-operations/kritika/internal/model"
	"github.com/home-operations/kritika/internal/textcut"
)

// Source bounds: how many URLs one run records, and how long each can be.
const (
	maxSources     = 50
	maxSourceBytes = 2048
)

// sourceEscaper percent-encodes what can end a Markdown autolink or a code
// span early, so that a recorded URL renders as one link and nothing else.
var sourceEscaper = strings.NewReplacer(" ", "%20", "<", "%3C", ">", "%3E", "`", "%60")

// RunConfig configures the run tool.
type RunConfig struct {
	// Dir is the checkout the commands run in.
	Dir string
	// Env is the commands' whole environment.
	Env []string
	// CommandEnv adds variables to one command's environment, by name.
	CommandEnv map[string][]string
	// Commands map each name that the model can run to the binary that it
	// runs.
	Commands map[string]string
	// Resolve, when set, finds the binary that a command runs and the
	// environment that it runs with, at the time of the call. It installs the
	// command's tool first when the tool is not installed yet. Commands then
	// only names what is offered. Resolve's error is returned to the model.
	Resolve func(ctx context.Context, command string) (bin string, env []string, err error)
	// Prepare, when set, writes Dir before the first command runs, so that a
	// review that runs none never pays for it. It runs once. Its note, when
	// any, opens every command's result, since the tool's description went
	// out before the note was known. Its error is returned to the model for
	// that command and every later one. An error that it returns because ctx
	// ended is not kept: the next command, with a context of its own, runs it
	// again. So Dir must be fit to write again after a failure.
	Prepare func(ctx context.Context) (note string, err error)
	// Timeout bounds one command.
	Timeout time.Duration
	// MaxOutputBytes caps the combined output kept of one command.
	MaxOutputBytes int
	// Proxied says the commands reach the network through the egress
	// gateway, which fetches a plain http:// URL over HTTPS. Hosts, when
	// set, name what the gateway lets them reach, "*" for every host.
	Proxied bool
	Hosts   []string
	// Note, when set, is appended to the tool's description.
	Note string
	// Mask, when set, is applied to a command's output and setup errors
	// before the model sees it. This keeps a credential that a command
	// printed out of the conversation.
	Mask func(string) string
	// Keep, when set, says where a cut output is kept whole.
	Keep *Kept
	// Logger, when set, logs each command as it starts, its arguments masked,
	// since the pod's own log is not. A step is logged when it completes.
	// Without this log, a runner killed mid-command, as by the memory limit,
	// leaves no trace of what it was running.
	Logger *slog.Logger
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

// WriteBudget is what the tools can write beside the checkout between them,
// in bytes: fetch_repo its fetched files and diffs, the run tool its kept
// outputs. The loop calls tools one at a time, so it is not locked.
type WriteBudget struct {
	left int64
}

// NewWriteBudget is a budget of n bytes.
func NewWriteBudget(n int64) *WriteBudget { return &WriteBudget{left: n} }

// Left is what can still be written.
func (b *WriteBudget) Left() int64 { return b.left }

// Spend counts n bytes written.
func (b *WriteBudget) Spend(n int64) { b.left -= n }

// RunTool executes one allowlisted binary with the model's arguments,
// directly and without a shell, in a checkout of the head commit. It records,
// as a source that the review consulted, every http(s) URL that curl is given
// and what each gh call reads. It also records which commands it ran.
type RunTool struct {
	cfg        RunConfig
	names      []string
	schema     json.RawMessage
	sources    []string
	ran        []string
	executions map[string]int
	// proxyHosts are the hosts of the proxies in the commands' environment,
	// as hostKey spells them: a URL of theirs given to curl, as a proxy
	// option's value, is not a source.
	proxyHosts []string
	// errors are the setup failure notes, one per command in failed.
	failed, errors []string
	// calls counts the commands run, which number the kept outputs.
	calls int
	// prepared is set once Prepare ran, prepareNote and prepareErr to what it
	// returned. The loop calls tools one at a time, so none is locked.
	prepared    bool
	prepareNote string
	prepareErr  error
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
	return &RunTool{cfg: c, names: names, schema: json.RawMessage(schema), executions: map[string]int{}, proxyHosts: proxyHosts(c.Env)}
}

// proxyHosts are the hosts of the HTTP_PROXY and HTTPS_PROXY URLs in env,
// in either case, as hostKey spells them.
func proxyHosts(env []string) []string {
	var hosts []string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.EqualFold(name, "HTTP_PROXY") && !strings.EqualFold(name, "HTTPS_PROXY") {
			continue
		}
		if u, err := url.Parse(value); err == nil && u.Hostname() != "" && !slices.Contains(hosts, hostKey(u.Hostname())) {
			hosts = append(hosts, hostKey(u.Hostname()))
		}
	}
	return hosts
}

// hostKey is a host as its spellings compare: lowercased, without a
// trailing dot.
func hostKey(host string) string { return strings.TrimSuffix(strings.ToLower(host), ".") }

// Names are the commands the tool offers, sorted.
func (rt *RunTool) Names() []string { return rt.names }

// Sources are the URLs curl was given and gh read, in first-use order,
// never nil.
func (rt *RunTool) Sources() []string { return append([]string{}, rt.sources...) }

// Ran are the commands the model ran, each once in first-use order, never
// nil.
func (rt *RunTool) Ran() []string { return append([]string{}, rt.ran...) }

// Executions counts processes started, nonzero exits and timeouts included.
// It excludes rejected commands and installation or process-start failures.
func (rt *RunTool) Executions() map[string]int { return maps.Clone(rt.executions) }

// Errors are masked command setup failures, the first per command, for
// the review summary.
func (rt *RunTool) Errors() []string { return append([]string{}, rt.errors...) }

func (rt *RunTool) setupError(command string, err error) error {
	// A path error names the binary under the runner's scratch space,
	// which the summary must not show.
	shown := err
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		shown = fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	note := textcut.Truncate(rt.mask(fmt.Sprintf("Command %s unavailable: %s", command, shown)), 2048)
	if !slices.Contains(rt.failed, command) {
		rt.failed, rt.errors = append(rt.failed, command), append(rt.errors, note)
	}
	if rt.cfg.Logger != nil {
		// The rest of an install's output is in the runner's own log of it.
		line, _, _ := strings.Cut(shown.Error(), "\n")
		rt.cfg.Logger.Info("command unavailable", "command", command, "error", rt.mask(line))
	}
	return commandSetupError{message: "agent: run: " + note, cause: err}
}

// commandSetupError masks its message while preserving cancellation and
// filesystem errors for callers that inspect the cause.
type commandSetupError struct {
	message string
	cause   error
}

func (e commandSetupError) Error() string { return e.message }
func (e commandSetupError) Unwrap() error { return e.cause }

// Kept is where the tool keeps a cut output whole, nil when it keeps none.
func (rt *RunTool) Kept() *Kept { return rt.cfg.Keep }

func (rt *RunTool) Def() model.ToolDef {
	desc := fmt.Sprintf("Run one of these commands in a checkout of the head commit: %s. The command runs directly, "+
		"without a shell: arguments are passed exactly as given, with no globbing, pipes or redirection. It is stopped "+
		"after %s, and its exit code and combined output are returned.", strings.Join(rt.names, ", "), rt.cfg.Timeout)
	if slices.Contains(rt.names, "rg") {
		desc += " rg searches file contents where the grep tool falls short: by file type (`rg -t go <pattern>`) " +
			"or across lines (`rg -U <pattern>`); for a few lines around a match, grep's context_lines is enough."
	}
	if slices.Contains(rt.names, "fd") {
		desc += " fd finds files and directories by name, extension or type (`fd -e yaml values`, `fd -t d charts`), " +
			"where the list_files tool's glob is not enough."
	}
	if slices.Contains(rt.names, "grep") {
		desc += " The grep command searches file contents where the grep tool falls short: by file type " +
			"(`grep -rn --include='*.go' <pattern> .`) or in what fetch_repo and a cut output write beside the checkout; " +
			"for a few lines around a match in the checkout, the grep tool's context_lines is enough."
	}
	if slices.Contains(rt.names, "find") {
		desc += " find lists files and directories by name, type or size (`find . -name '*.yaml'`, `find . -type d -name charts`), " +
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
		desc += " Use gh, not curl, for anything on GitHub: it is signed in with a token that reads the repository under " +
			"review and public repositories, such as `gh release view <tag> -R <owner>/<repo>` for a release's notes or " +
			"`gh pr view <number> -R <owner>/<repo>` for an upstream pull request. Any other private repository is out " +
			"of its reach, by the API or GraphQL alike. The repository goes after -R, never as a second argument, and " +
			"--json takes the fields to print, as in `gh release view v1.2.0 -R <owner>/<repo> --json tagName,body`."
	}
	if slices.Contains(rt.names, "curl") {
		if rt.cfg.Proxied {
			desc += " The network is reached through a gateway that allows only " + AllowedHosts(rt.cfg.Hosts) + ": a request " +
				"to any other host fails with a 403 naming the refusal, and another host of the same kind is refused too. " +
				"Give curl http:// URLs: the gateway fetches them over HTTPS and authenticates to the hosts it holds a " +
				"credential for."
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

// AllowedHosts words what the gateway allows for a tool's description:
// the hosts it lists, "some hosts" when none are known, and every host
// but a few when "*" is among them.
func AllowedHosts(hosts []string) string {
	switch {
	case slices.Contains(hosts, "*"):
		return "every host but a few"
	case len(hosts) > 0:
		return "these hosts: " + strings.Join(hosts, ", ")
	}
	return "some hosts"
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
	if !slices.Contains(rt.ran, req.Command) {
		rt.ran = append(rt.ran, req.Command)
	}

	rt.calls++
	if rt.cfg.Logger != nil {
		args := make([]string, len(req.Args))
		for i, a := range req.Args {
			args[i] = rt.mask(a)
		}
		rt.cfg.Logger.Info("command started", "call", rt.calls, "command", req.Command, "args", args)
	}
	note, err := rt.prepare(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("agent: run: %w", ctx.Err())
		}
		return "", rt.setupError(req.Command, err)
	}
	env := rt.cfg.Env
	if rt.cfg.Resolve != nil {
		if bin, env, err = rt.cfg.Resolve(ctx, req.Command); err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("agent: run: %w", ctx.Err())
			}
			return "", rt.setupError(req.Command, err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, rt.cfg.Timeout)
	defer cancel()
	out := &cappedBuffer{max: rt.cfg.MaxOutputBytes, keep: rt.cfg.MaxOutputBytes}
	if k := rt.cfg.Keep; k != nil {
		// An output that will not be kept is not buffered past the cut.
		out.keep = max(out.max, int(min(int64(k.FileBytes), k.Budget.Left())))
	}
	errs := &stderrTail{out: out, max: rt.cfg.MaxOutputBytes / stderrShare, last: '\n'}
	cmd := exec.CommandContext(cctx, bin, req.Args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = rt.cfg.Dir, append(slices.Clone(env), rt.cfg.CommandEnv[req.Command]...), out, errs
	// The command and the processes that it starts form a process group of
	// their own. The group is stopped as a whole when the command times out
	// and once it exits (stopGroupOnExit), so nothing that it started
	// outlives the call. The command is reaped only after that, so the
	// group's id stays its own while the group is signaled.
	var group groupSignals
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return group.kill(cmd.Process) }
	// Once the command is killed, a child still holding its output open is
	// not waited for.
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err == nil {
		rt.executions[req.Command]++
		stopped, serr := stopGroupOnExit(cmd.Process.Pid)
		if serr != nil && rt.cfg.Logger != nil {
			rt.cfg.Logger.Warn("what a command started is not stopped on its exit", "command", req.Command, "error", serr)
		}
		if stopped {
			group.exit()
		} else {
			group.leaderOnly()
		}
		err = cmd.Wait()
	}
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("agent: run: %w", ctx.Err())
	case cctx.Err() != nil:
		stopped := fmt.Sprintf("%sstopped after %s\n%s", note, rt.cfg.Timeout, rt.mask(out.String()))
		return rt.result(stopped, out, out.dropped(), rt.mask(errs.String())), nil
	// A process that left the group still held the output open when the
	// command exited: the command ran, and its output is what it wrote.
	case errors.Is(err, exec.ErrWaitDelay):
	case err != nil:
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			return "", rt.setupError(req.Command, err)
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
	return rt.result(fmt.Sprintf("%sexit code %d\n%s", note, code, text), out, dropped, stderr), nil
}

// groupSignals is what signals a command's process group, a timeout, until
// the command is about to be reaped. The reap can free the group's id for
// another group. From then on, it signals nothing, or the command alone,
// which os.Process signals safely after the reap too. A kill in flight
// finishes before either.
type groupSignals struct {
	mu sync.Mutex
	// exited says that the command's exit was seen and its group stopped.
	// leader says that the exit was not seen before the reap.
	exited, leader bool
}

// kill kills the process group the command p leads.
func (g *groupSignals) kill(p *os.Process) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.exited:
		return os.ErrProcessDone
	case g.leader:
		return p.Kill()
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// exit says the command's exit was seen and its group stopped.
func (g *groupSignals) exit() {
	g.mu.Lock()
	g.exited = true
	g.mu.Unlock()
}

// leaderOnly says the command will be reaped without its exit seen first,
// so a timeout from then on kills it alone.
func (g *groupSignals) leaderOnly() {
	g.mu.Lock()
	g.leader = true
	g.mu.Unlock()
}

// prepare runs Prepare before the first command and returns its note, as a
// line of its own, for every command. Its failure stands for every command
// after it, unless ctx ended it.
func (rt *RunTool) prepare(ctx context.Context) (string, error) {
	if rt.cfg.Prepare == nil {
		return "", nil
	}
	if !rt.prepared {
		note, err := rt.cfg.Prepare(ctx)
		if err != nil && ctx.Err() != nil {
			return "", err
		}
		if note != "" {
			note += "\n"
		}
		rt.prepared, rt.prepareNote, rt.prepareErr = true, note, err
	}
	return rt.prepareNote, rt.prepareErr
}

// A cut output keeps at most MaxOutputBytes/stderrShare of the end of the
// command's stderr, after stderrLead.
const (
	stderrShare = 8
	stderrLead  = "\n[the end of stderr, from the part cut above:]\n"
)

// result is a command's result cut to MaxOutputBytes with its note. The note
// also counts the bytes that the buffer dropped, so the loop's own cut to the
// same limit leaves the result whole. A cut output is then kept whole in a
// file, from out. A note after the cut's note names that file. stderr is the
// end of the stderr that the buffer dropped, where an error printed after a
// long output lands. It follows the notes, with the rest cut shorter to make
// room, unless it takes more than a quarter of the limit.
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

// keep writes the whole output in out, masked, to the file for this call. It
// returns the note, which names the file or says why the output is not kept:
// the output is over what one file can hold, or past what the review can
// write.
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
// credentials it carries, but one on a proxy's host.
func (rt *RunTool) record(args []string) {
	for _, a := range args {
		u, err := url.Parse(a)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			slices.Contains(rt.proxyHosts, hostKey(u.Hostname())) {
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

// String is the output that the result returns as valid UTF-8. dropped counts
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
	// before is the stderr byte just before the tail, and last the last one
	// written. '\n' stands for the start of the stream.
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

// String is the kept end of stderr from its first whole line, as valid UTF-8.
// A line cut at its start is left out: the mask applied to it recognizes only
// whole credentials. If the start of a credential is cut there, the rest of
// it stays unmasked.
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
