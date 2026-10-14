//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// The finite worker is independent of the calling terminal, not an installed service.
func configureTerminalWorker(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
