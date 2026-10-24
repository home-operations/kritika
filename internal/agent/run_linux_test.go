package agent

import (
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

// TestGroupSignals: a timeout kills the command's group until the command's
// exit has been seen, and nothing after.
func TestGroupSignals(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := func() *exec.Cmd {
		cmd := exec.Command(self, "sleep")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	var group groupSignals
	running := start()
	if err := group.kill(running.Process.Pid); err != nil {
		t.Fatalf("kill = %v", err)
	}
	if err := running.Wait(); err == nil || running.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the group was not killed: %v", err)
	}
	group.exit()
	spared := start()
	if err := group.kill(spared.Process.Pid); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill after exit = %v, want os.ErrProcessDone", err)
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
