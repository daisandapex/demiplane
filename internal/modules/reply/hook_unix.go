// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package reply

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the hook as the leader of a new process group and
// makes context cancellation (the timeout) kill the whole group, not only the
// shell.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}
}

// killProcessGroup kills every process left in the hook's group. A group that
// is already gone (ESRCH) is the normal case after a clean exit.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
