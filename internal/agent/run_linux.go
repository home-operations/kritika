package agent

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// stopGroupOnExit waits for the command pid to exit without reaping it,
// which keeps its process group's id from being reused, then kills what is
// left of the group: the processes the command started that outlived it.
// A wait that fails for another reason than a signal stops nothing.
func stopGroupOnExit(pid int) {
	var info unix.Siginfo
	for {
		switch err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); {
		case err == nil:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			return
		case !errors.Is(err, unix.EINTR):
			return
		}
	}
}
