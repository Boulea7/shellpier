//go:build !windows

package access

import (
	"os"
	"syscall"
)

// Prevent a replacement FIFO from blocking between the metadata check and open.
func openTicket(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

func statTicket(path string) (os.FileInfo, error) { return os.Lstat(path) }
