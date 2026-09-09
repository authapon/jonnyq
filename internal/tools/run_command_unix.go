//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup makes cmd the leader of a new process group (its
// pgid becomes its own pid). bash -c runs non-interactively, so job control
// is off and any "command &" it backgrounds stays in that same group -
// which is what lets killProcessGroup reap it too.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the whole process group instead of just
// the direct child, so a backgrounded grandchild that inherited the
// stdout/stderr pipe gets killed too. Without this, that grandchild keeps
// the pipe open after bash exits and cmd.Wait() hangs until it finishes on
// its own, ignoring the configured timeout entirely.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
