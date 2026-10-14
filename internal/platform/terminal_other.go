//go:build !darwin && !linux && !windows

package platform

import "context"

func TerminalSupported() bool { return false }
func startTerminal(context.Context, TerminalOptions) (Terminal, error) {
	return nil, ErrTerminalUnsupported
}
