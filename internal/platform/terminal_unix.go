//go:build darwin || linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TerminalSupported() bool { return true }

type unixTerminal struct {
	file    *os.File
	cmd     *exec.Cmd
	mu      sync.Mutex
	closed  bool
	done    chan struct{}
	code    uint32
	waitErr error
}

func startTerminal(ctx context.Context, o TerminalOptions) (Terminal, error) {
	var cmd *exec.Cmd
	var err error
	if o.Command != "" {
		cmd, err = Command(o.Shell, o.Command, o.WorkDir)
	} else {
		switch o.Shell {
		case "", "auto":
			shell := os.Getenv("SHELL")
			if !filepath.IsAbs(shell) {
				shell = "/bin/sh"
			} else if info, e := os.Stat(shell); e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				shell = "/bin/sh"
			}
			cmd = exec.Command(shell, "-i")
		case "sh":
			cmd = exec.Command("sh", "-i")
		default:
			return nil, fmt.Errorf("unsupported interactive shell %q on Unix", o.Shell)
		}
		cmd.Dir = o.WorkDir
	}
	if err != nil {
		return nil, err
	}
	cmd.Env, err = terminalEnvironment(o)
	if err != nil {
		return nil, err
	}
	file, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	if err = pty.Setsize(file, &pty.Winsize{Cols: uint16(o.Columns), Rows: uint16(o.Rows)}); err != nil {
		file.Close()
		return nil, err
	}
	if err = applyTerminalModes(tty, o.Modes); err != nil {
		file.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err = cmd.Start(); err != nil {
		file.Close()
		return nil, err
	}

	t := &unixTerminal{file: file, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		var code uint32
		var waitErr error
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				status := exit.Sys().(syscall.WaitStatus)
				if status.Signaled() {
					code = 128 + uint32(status.Signal())
				} else {
					code = uint32(status.ExitStatus())
				}
			} else {
				waitErr = err
			}
		}
		t.mu.Lock()
		t.code, t.waitErr = code, waitErr
		t.mu.Unlock()
		close(t.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = t.Close()
		case <-t.done:
		}
	}()
	return t, nil
}
func (t *unixTerminal) Read(b []byte) (int, error) {
	n, err := t.file.Read(b)
	if errors.Is(err, syscall.EIO) {
		err = io.EOF
	}
	return n, err
}
func (t *unixTerminal) Write(b []byte) (int, error) { return t.file.Write(b) }
func (t *unixTerminal) Resize(columns, rows uint32) error {
	if err := terminalSize(columns, rows); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	return pty.Setsize(t.file, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)})
}
func (t *unixTerminal) Signal(name string) error {
	if !terminalSignalAllowed(name) {
		return fmt.Errorf("unsupported terminal signal %q", name)
	}
	signals := map[string]syscall.Signal{"ABRT": syscall.SIGABRT, "ALRM": syscall.SIGALRM, "FPE": syscall.SIGFPE, "HUP": syscall.SIGHUP, "ILL": syscall.SIGILL, "INT": syscall.SIGINT, "KILL": syscall.SIGKILL, "PIPE": syscall.SIGPIPE, "QUIT": syscall.SIGQUIT, "SEGV": syscall.SIGSEGV, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	group, err := unix.IoctlGetInt(int(t.file.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return err
	}
	if group <= 0 {
		return errors.New("terminal has no foreground process group")
	}
	return syscall.Kill(-group, signals[name])
}
func (t *unixTerminal) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	var failures []error
	session := t.cmd.Process.Pid
	// Freeze the shell before enumerating jobs so it cannot create another
	// ordinary job-control group while teardown is collecting this session.
	if err := syscall.Kill(-session, syscall.SIGSTOP); err != nil && err != syscall.ESRCH {
		failures = append(failures, err)
	}
	groups, err := terminalSessionGroups(session)
	if err != nil {
		failures = append(failures, fmt.Errorf("enumerate terminal session jobs: %w", err))
	}
	for _, group := range groups {
		if group == session {
			continue
		}
		if err := syscall.Kill(-group, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			failures = append(failures, err)
		}
	}
	if err := syscall.Kill(-session, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		failures = append(failures, err)
	}
	failures = append(failures, t.file.Close())
	return errors.Join(failures...)
}
func (t *unixTerminal) Wait() (uint32, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.code, t.waitErr
}

func applyTerminalModes(tty *os.File, modes map[uint8]uint32) error {
	if len(modes) == 0 {
		return nil
	}
	attr, err := unix.IoctlGetTermios(int(tty.Fd()), terminalGetAttrs)
	if err != nil {
		return err
	}
	for opcode, value := range modes {
		switch opcode {
		case 1, 5:
			if value > 255 {
				return errors.New("terminal control character exceeds 255")
			}
			if opcode == 1 {
				attr.Cc[unix.VINTR] = uint8(value)
			} else {
				attr.Cc[unix.VEOF] = uint8(value)
			}
		case 50:
			if value != 0 {
				attr.Lflag |= unix.ISIG
			} else {
				attr.Lflag &^= unix.ISIG
			}
		case 51:
			if value != 0 {
				attr.Lflag |= unix.ICANON
			} else {
				attr.Lflag &^= unix.ICANON
			}
		case 53:
			if value != 0 {
				attr.Lflag |= unix.ECHO
			} else {
				attr.Lflag &^= unix.ECHO
			}
		case 36:
			if value != 0 {
				attr.Iflag |= unix.ICRNL
			} else {
				attr.Iflag &^= unix.ICRNL
			}
		case 72:
			if value != 0 {
				attr.Oflag |= unix.ONLCR
			} else {
				attr.Oflag &^= unix.ONLCR
			}
		}
	}
	return unix.IoctlSetTermios(int(tty.Fd()), terminalSetAttrs, attr)
}
