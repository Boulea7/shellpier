//go:build darwin

package platform

import "golang.org/x/sys/unix"

const terminalGetAttrs = unix.TIOCGETA
const terminalSetAttrs = unix.TIOCSETA
