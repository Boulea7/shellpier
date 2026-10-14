//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Retain inherited explicit pipes and CI jobs, but do not inherit the caller's Ctrl+C group.
func configureTerminalWorker(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// Lstat IDs are lazy on Windows; handle.Stat captures the original file index now.
func terminalFileIdentity(path string) (os.FileInfo, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
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
func terminalRetryReplace(err error) bool {
	// MoveFileEx reports ACCESS_DENIED as well as SHARING_VIOLATION when
	// replacing an existing destination held without FILE_SHARE_DELETE.
	// The caller rechecks the original identity and private DACL on every attempt.
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
