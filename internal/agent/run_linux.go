package agent

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// stopGroupOnExit waits for the command pid to exit without reaping it. This
// keeps the id of its process group from being reused. It then kills what is
// left of the group: the processes that the command started and that outlived
// it. stopped says that it did this. A wait that fails stops nothing, and its
// error says why.
func stopGroupOnExit(pid int) (stopped bool, err error) {
	var info unix.Siginfo
	for {
		switch err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); {
		case err == nil:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			return true, nil
		case !errors.Is(err, unix.EINTR):
			return false, err
		}
	}
}
