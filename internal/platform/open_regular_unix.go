//go:build !windows

package platform

import (
	"os"
	"syscall"
)

// OpenRegular opens without following symlinks or blocking on a replacement FIFO.
func OpenRegular(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
