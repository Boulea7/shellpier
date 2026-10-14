//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TerminalSupported() bool {
	dll := windows.NewLazySystemDLL("kernel32.dll")
	for _, name := range []string{"CreatePseudoConsole", "ResizePseudoConsole", "ClosePseudoConsole"} {
		if dll.NewProc(name).Find() != nil {
			return false
		}
	}
	return true
}

type windowsTerminal struct {
	input, output *os.File
	reader        *io.PipeReader
	writer        *io.PipeWriter
	mu            sync.Mutex
	console, job  windows.Handle
	closed        bool
	closeOnce     sync.Once
	closeErr      error
	consoleOnce   sync.Once
	done          chan struct{}
	code          uint32
	waitErr       error
}

func terminalWindowsCommand(o TerminalOptions) (*exec.Cmd, error) {
	if o.Command != "" {
		return Command(o.Shell, o.Command, o.WorkDir)
	}
	shell := o.Shell
	if shell == "" || shell == "auto" {
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			shell = "powershell"
		} else {
			shell = "cmd"
		}
	}
	var cmd *exec.Cmd
	switch shell {
	case "powershell":
		cmd = exec.Command("powershell.exe", "-NoLogo", "-NoProfile")
	case "cmd":
		cmd = exec.Command("cmd.exe", "/d")
	case "sh":
		cmd = exec.Command("sh", "-i")
	default:
		return nil, fmt.Errorf("unsupported interactive shell %q", shell)
	}
	cmd.Dir = o.WorkDir
	return cmd, nil
}
func startTerminal(ctx context.Context, o TerminalOptions) (Terminal, error) {
	if !TerminalSupported() {
		return nil, ErrTerminalUnsupported
	}
	cmd, err := terminalWindowsCommand(o)
	if err != nil {
		return nil, err
	}
	env, err := terminalEnvironment(o)
	if err != nil {
		return nil, err
	}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	envBlock := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		return nil, err
	}
	var dir *uint16
	if o.WorkDir != "" {
		dir, err = windows.UTF16PtrFromString(o.WorkDir)
		if err != nil {
			return nil, err
		}
	}
	t := &windowsTerminal{done: make(chan struct{})}
	t.reader, t.writer = io.Pipe()
	success := false
	var pi windows.ProcessInformation
	defer func() {
		if !success {
			if pi.Process != 0 {
				_ = windows.TerminateProcess(pi.Process, 1)
				_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
				_ = windows.CloseHandle(pi.Process)
			}
			if pi.Thread != 0 {
				_ = windows.CloseHandle(pi.Thread)
			}
			_ = t.Close()
		}
	}()
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err = windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(inputRead)
	t.input = os.NewFile(uintptr(inputWrite), "ConPTY input")
	if err = windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(outputWrite)
	t.output = os.NewFile(uintptr(outputRead), "ConPTY output")
	// Keep draining on its own goroutine, including after explicit Close discards
	// output, so ClosePseudoConsole cannot deadlock while emitting its final frame.
	go func() {
		defer t.output.Close()
		defer t.writer.Close()
		buffer := make([]byte, 32768)
		discard := false
		for {
			n, err := t.output.Read(buffer)
			if n > 0 && !discard {
				if _, writeErr := t.writer.Write(buffer[:n]); writeErr != nil {
					discard = true
				}
			}
			if err != nil {
				return
			}
		}
	}()
	if err = windows.CreatePseudoConsole(windows.Coord{X: int16(o.Columns), Y: int16(o.Rows)}, inputRead, outputWrite, 0, &t.console); err != nil {
		return nil, fmt.Errorf("create ConPTY: %w", err)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// This attribute's value is the HPCON handle itself, not a pointer to it.
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&t.console)), unsafe.Sizeof(t.console)); err != nil {
		return nil, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	t.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(t.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return nil, err
	}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err = windows.CreateProcess(app, command, nil, nil, false, flags, &envBlock[0], dir, &si.StartupInfo, &pi); err != nil {
		return nil, err
	}
	if err = windows.AssignProcessToJobObject(t.job, pi.Process); err != nil {
		return nil, err
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		return nil, err
	}
	_ = windows.CloseHandle(pi.Thread)
	pi.Thread = 0
	success = true
	go func() {
		_, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		var code uint32
		if waitErr == nil {
			waitErr = windows.GetExitCodeProcess(pi.Process, &code)
		}
		_ = windows.CloseHandle(pi.Process)
		t.mu.Lock()
		t.code, t.waitErr = code, waitErr
		if t.job != 0 {
			_ = windows.CloseHandle(t.job)
			t.job = 0
		}
		t.mu.Unlock()
		close(t.done)
		t.closeConsole()
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
func (t *windowsTerminal) closeConsole() {
	t.consoleOnce.Do(func() {
		t.mu.Lock()
		console := t.console
		t.console = 0
		t.mu.Unlock()
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
	})
}
func (t *windowsTerminal) Read(b []byte) (int, error) { return t.reader.Read(b) }
func (t *windowsTerminal) Write(b []byte) (int, error) {
	t.mu.Lock()
	input := t.input
	closed := t.closed
	t.mu.Unlock()
	if closed || input == nil {
		return 0, os.ErrClosed
	}
	return input.Write(b)
}
func (t *windowsTerminal) Resize(columns, rows uint32) error {
	if err := terminalSize(columns, rows); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.console == 0 {
		return os.ErrClosed
	}
	return windows.ResizePseudoConsole(t.console, windows.Coord{X: int16(columns), Y: int16(rows)})
}
func (t *windowsTerminal) Signal(name string) error {
	if !terminalSignalAllowed(name) {
		return fmt.Errorf("unsupported terminal signal %q", name)
	}
	switch name {
	case "INT":
		_, err := t.Write([]byte{3})
		return err
	case "TERM", "KILL":
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.closed || t.job == 0 {
			return os.ErrClosed
		}
		return windows.TerminateJobObject(t.job, 1)
	default:
		return fmt.Errorf("terminal signal %s is unsupported by ConPTY", name)
	}
}
func (t *windowsTerminal) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		job := t.job
		t.job = 0
		input := t.input
		t.mu.Unlock()
		_ = t.reader.Close()
		var failures []error
		if input != nil {
			failures = append(failures, input.Close())
		}
		if job != 0 {
			failures = append(failures, windows.CloseHandle(job))
		}
		t.closeConsole()
		if t.output != nil {
			_ = t.output.Close()
		}
		_ = t.writer.Close()
		t.closeErr = errors.Join(failures...)
	})
	return t.closeErr
}
func (t *windowsTerminal) Wait() (uint32, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.code, t.waitErr
}
