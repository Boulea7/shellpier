//go:build linux

package netutil

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
)

func bindInterface(fd uintptr, network string, nic *net.Interface) error {
	if err := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, nic.Name); err != nil {
		return fmt.Errorf("SO_BINDTODEVICE for %q failed (may require CAP_NET_RAW): %w", nic.Name, err)
	}
	return nil
}
