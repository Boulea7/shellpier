//go:build !windows

package access

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestRetryUnixErrnos(t *testing.T) {
	for _, code := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ENETRESET, syscall.ETIMEDOUT, syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EPIPE} {
		wrapped := fmt.Errorf("ssh: handshake failed: %w", &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", code)})
		if !retryableConnection(wrapped) {
			t.Fatalf("POSIX network error stopped retrying: %v", code)
		}
	}
}
