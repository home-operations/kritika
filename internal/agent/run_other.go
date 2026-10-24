//go:build !linux

package agent

// stopGroupOnExit stops nothing off Linux, where a command's process group
// is stopped only when the command times out.
func stopGroupOnExit(int) (stopped bool, err error) { return false, nil }
