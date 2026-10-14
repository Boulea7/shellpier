package access

import (
	"errors"

	"golang.org/x/sys/windows"
)

// WinSock uses WSA error numbers rather than the portable POSIX errno values.
func retryablePlatformConnection(err error) bool {
	for _, code := range []error{windows.WSAECONNREFUSED, windows.WSAECONNRESET, windows.WSAECONNABORTED, windows.WSAENETRESET, windows.WSAETIMEDOUT, windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}
