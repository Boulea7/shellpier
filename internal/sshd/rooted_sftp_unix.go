//go:build !windows

package sshd

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func rootedOpen(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flags|unix.O_NONBLOCK, mode)
}
func rootedFileTimes(file *os.File, atime, mtime time.Time) error {
	return unix.Futimes(int(file.Fd()), []unix.Timeval{unix.NsecToTimeval(atime.UnixNano()), unix.NsecToTimeval(mtime.UnixNano())})
}
