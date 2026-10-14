//go:build !darwin && !linux

package netutil

import (
	"fmt"
	"net"
)

func bindInterface(fd uintptr, network string, nic *net.Interface) error {
	return fmt.Errorf("explicit interface routing is not supported on this operating system")
}
