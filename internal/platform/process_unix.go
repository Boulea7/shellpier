//go:build !windows

package platform

import (
	"os/exec"
	"sync"
	"syscall"
)

// Start starts a private process group and returns an idempotent tree terminator.
func Start(cmd *exec.Cmd) (func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			result = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if result == syscall.ESRCH {
				result = nil
			}
		})
		return result
	}, nil
}
