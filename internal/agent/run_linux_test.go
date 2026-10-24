package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
