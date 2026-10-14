//go:build !windows

package access

import (
	"errors"
	"syscall"
)

func retryablePlatformConnection(err error) bool {
	for _, code := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ENETRESET, syscall.ETIMEDOUT, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EPIPE} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}
