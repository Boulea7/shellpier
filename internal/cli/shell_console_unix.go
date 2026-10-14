//go:build !windows

package cli

import "io"

func terminalOutputMode(io.Writer) (func() error, error) {
	return func() error { return nil }, nil
}
