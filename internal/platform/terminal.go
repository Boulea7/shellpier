package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
)

var ErrTerminalUnsupported = errors.New("PTY is not supported on this system")

type Terminal interface {
	io.ReadWriteCloser
	Resize(columns, rows uint32) error
	Signal(name string) error
	Wait() (uint32, error)
}
type TerminalOptions struct {
	Shell, Command, WorkDir, Term string
	Columns, Rows                 uint32
	Env                           []string
	Modes                         map[uint8]uint32
}

func StartTerminal(ctx context.Context, o TerminalOptions) (Terminal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := terminalSize(o.Columns, o.Rows); err != nil {
		return nil, err
	}
	if len(o.Term) > 128 || strings.ContainsAny(o.Term, "\x00\r\n") {
		return nil, errors.New("TERM must be at most 128 bytes without NUL or line breaks")
	}
	if strings.ContainsRune(o.Command, 0) || strings.ContainsRune(o.WorkDir, 0) {
		return nil, errors.New("terminal command and directory must not contain NUL")
	}
	if _, err := terminalEnvironment(o); err != nil {
		return nil, err
	}
	return startTerminal(ctx, o)
}
func terminalSize(columns, rows uint32) error {
	if columns < 1 || columns > 32767 || rows < 1 || rows > 32767 {
		return errors.New("terminal dimensions must be between 1 and 32767")
	}
	return nil
}
func terminalEnvironment(o TerminalOptions) ([]string, error) {
	env := append([]string(nil), os.Environ()...)
	overrides := append([]string(nil), o.Env...)
	if o.Term != "" {
		overrides = append(overrides, "TERM="+o.Term)
	}
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) {
			return nil, errors.New("terminal environment requires nonempty NAME=VALUE without NUL")
		}
		found := false
		for i, old := range env {
			oldKey, _, _ := strings.Cut(old, "=")
			matches := oldKey == key
			if runtime.GOOS == "windows" {
				matches = strings.EqualFold(oldKey, key)
			}
			if matches {
				env[i] = entry
				found = true
				break
			}
		}
		if !found {
			env = append(env, entry)
		}
	}
	return env, nil
}
func terminalSignalAllowed(name string) bool {
	switch name {
	case "ABRT", "ALRM", "FPE", "HUP", "ILL", "INT", "KILL", "PIPE", "QUIT", "SEGV", "TERM", "USR1", "USR2":
		return true
	}
	return false
}

func TerminalSignals() []string {
	if !TerminalSupported() {
		return []string{}
	}
	if runtime.GOOS == "windows" {
		return []string{"INT", "TERM", "KILL"}
	}
	return []string{"ABRT", "ALRM", "FPE", "HUP", "ILL", "INT", "KILL", "PIPE", "QUIT", "SEGV", "TERM", "USR1", "USR2"}
}
