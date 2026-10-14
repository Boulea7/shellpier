package access

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/Boulea7/shellpier/internal/relay"
	"golang.org/x/sys/windows"
)

func TestRetryWindowsWinSockErrors(t *testing.T) {
	for _, code := range []error{windows.WSAECONNABORTED, windows.WSAECONNRESET, windows.WSAECONNREFUSED, windows.WSAENETRESET, windows.WSAETIMEDOUT, windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH} {
		// Match the net.OpError -> os.SyscallError chain returned by wsarecv.
		recv := &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("wsarecv", code)}
		handshake := fmt.Errorf("ssh: handshake failed: %w", recv)
		if !errors.Is(handshake, code) {
			t.Fatalf("WinSock error identity was lost: %v", code)
		}
		if !retryableConnection(handshake) {
			t.Fatalf("transient WinSock error was not retried: %v", code)
		}
	}
}
func TestRetryWindowsDoesNotRetryPermanentFailures(t *testing.T) {
	for _, err := range []error{windows.WSAEACCES, windows.WSAEINVAL, windows.ERROR_ACCESS_DENIED, x509.UnknownAuthorityError{}, relay.ErrUnauthorized, relay.ErrSessionClosed, relay.ErrRedirectDenied, errors.New("ssh: unable to authenticate"), errors.New("ssh: host key mismatch"), errors.Join(windows.WSAECONNABORTED, x509.UnknownAuthorityError{}), errors.Join(windows.WSAECONNABORTED, relay.ErrUnauthorized)} {
		if retryableConnection(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("permanent Windows failure was retried: %v", err)
		}
	}
}
