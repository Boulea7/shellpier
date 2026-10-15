package sshd

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func rootedOpen(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flags, mode)
}
func rootedFileTimes(file *os.File, atime, mtime time.Time) error {
	a, m := windows.NsecToFiletime(atime.UnixNano()), windows.NsecToFiletime(mtime.UnixNano())
	// Reopen the same file object, including directories, with metadata access.
	handle, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile").Call(file.Fd(), windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if handle == ^uintptr(0) {
		return err
	}
	defer windows.CloseHandle(windows.Handle(handle))
	return windows.SetFileTime(windows.Handle(handle), nil, &a, &m)
}
