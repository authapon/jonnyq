//go:build windows

package tools

import "os/exec"

// Windows has no POSIX process groups, and run_command already hardcodes a
// bash shell (unavailable natively on Windows regardless), so this is a
// plain single-process fallback rather than a true group kill.
func configureProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
