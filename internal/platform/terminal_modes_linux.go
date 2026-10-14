//go:build linux

package platform

import "golang.org/x/sys/unix"

const terminalGetAttrs = unix.TCGETS
const terminalSetAttrs = unix.TCSETS
