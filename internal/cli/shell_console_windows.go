package cli

import (
	"io"

	"golang.org/x/sys/windows"
)

func terminalOutputMode(output io.Writer) (func() error, error) {
	fd, ok := streamFD(output)
	if !ok {
		return func() error { return nil }, nil
	}
	handle := windows.Handle(fd)
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		return func() error { return nil }, nil
	}
	if err := windows.SetConsoleMode(handle, original|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(handle, original) }, nil
}
