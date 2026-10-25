package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunToolStopsWhatACommandStarted: a process a command started does
// not outlive the call, whether the command exited, holding its output
// open or not, or was stopped; one that left the command's process group
// does, and the command's output is its result all the same.
func TestRunToolStopsWhatACommandStarted(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		timeout time.Duration
		stopped bool
	}{
		{"after the command exits", []string{"spawn"}, 5 * time.Second, true},
		{"after the command exits, holding its output", []string{"spawn", "inherit"}, 5 * time.Second, true},
		{"when the command is stopped", []string{"spawn", "wait"}, 500 * time.Millisecond, true},
		{"not once it left the group, holding the output", []string{"spawn", "setsid"}, 5 * time.Second, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt, _ := newTestRunTool(t, false)
			rt.cfg.Timeout = tt.timeout
			input, err := json.Marshal(map[string]any{"command": "rg", "args": tt.args})
			if err != nil {
				t.Fatal(err)
			}
			out, err := rt.Run(t.Context(), input)
			_, rest, found := strings.Cut(out, "child=")
			pid, convErr := strconv.Atoi(strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0]))
			if err != nil || !found || convErr != nil {
				t.Fatalf("out = %q, %v", out, err)
			}
			if !tt.stopped {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				if !strings.HasPrefix(out, "exit code 0\n") {
					t.Fatalf("out = %q, want the command's exit and output", out)
				}
				return
			}
			for deadline := time.Now().Add(5 * time.Second); !exited(pid); time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Fatalf("the process the command started, %d, outlived the call", pid)
				}
			}
		})
	}
}

// TestGroupSignals: a timeout kills the command's group, the command alone
// once its exit cannot be seen before its reap, and nothing once it was.
func TestGroupSignals(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// start runs the helper with args as a group's leader, and returns it
	// and the pid of the child it started, if any.
	start := func(args ...string) (*exec.Cmd, int) {
		cmd := exec.Command(self, args...)
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// The leader may be reaped by then, so not its group: a child each
		// case leaves running has a cleanup of its own.
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		child := 0
		if args[0] == "spawn" {
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if child, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "child="))); err != nil {
				t.Fatalf("line %q: %v", line, err)
			}
		}
		return cmd, child
	}
	killed := func(cmd *exec.Cmd) bool {
		return cmd.Wait() != nil && cmd.ProcessState.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL
	}

	var whole groupSignals
	leader, child := start("spawn", "wait")
	if err := whole.kill(leader.Process); err != nil || !killed(leader) {
		t.Fatalf("kill = %v; the leader was not killed", err)
	}
	for deadline := time.Now().Add(5 * time.Second); !exited(child); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the group's other process, %d, was not killed", child)
		}
	}

	var alone groupSignals
	alone.leaderOnly()
	leader, child = start("spawn", "wait")
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if err := alone.kill(leader.Process); err != nil || !killed(leader) {
		t.Fatalf("kill = %v; the leader was not killed", err)
	}
	if exited(child) {
		t.Fatalf("the group's other process, %d, was killed", child)
	}

	var done groupSignals
	done.exit()
	spared, _ := start("sleep")
	if err := done.kill(spared.Process); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after the exit = %v, want os.ErrProcessDone", err)
	}
	if err := syscall.Kill(spared.Process.Pid, 0); err != nil {
		t.Fatalf("a group was signalled after the exit: %v", err)
	}
}

// exited reports whether pid has exited: reaped, or a zombie its new
// parent has yet to reap.
func exited(pid int) bool {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// The state follows the command name, which is in parentheses.
		s := string(stat)
		i := strings.LastIndexByte(s, ')')
		return i > 0 && i+2 < len(s) && (s[i+2] == 'Z' || s[i+2] == 'X')
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
