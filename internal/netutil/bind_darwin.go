//go:build darwin

package netutil

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
)

func bindInterface(fd uintptr, network string, nic *net.Interface) error {
	level, option := unix.IPPROTO_IP, unix.IP_BOUND_IF
	if network == "tcp6" {
		level, option = unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF
	}
	if err := unix.SetsockoptInt(int(fd), level, option, nic.Index); err != nil {
		return fmt.Errorf("bind socket to interface %q: %w", nic.Name, err)
	}
	return nil
}
