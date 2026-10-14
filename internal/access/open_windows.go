//go:build windows

package access

import "os"

func openTicket(path string) (*os.File, error) { return os.Open(path) }
