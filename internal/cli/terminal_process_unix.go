//go:build !windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// The finite worker is independent of the calling terminal, not an installed service.
func configureTerminalWorker(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Freeze identity from the opened object, never from a later lookup of its name.
func terminalFileIdentity(path string) (os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, errors.New("terminal resource is not a regular file or directory")
	}
	return info, nil
}
func terminalRetryReplace(error) bool { return false }
