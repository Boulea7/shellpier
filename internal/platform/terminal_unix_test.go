//go:build darwin || linux

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestTerminalUnixInteractiveResizeAndExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term, err := StartTerminal(ctx, TerminalOptions{Shell: "sh", Term: "xterm", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	output := captureTerminal(t, term)
	const readyPrompt = "__NEXT_READY_PROMPT__ "
	var encoded strings.Builder
	for _, char := range []byte(readyPrompt) {
		fmt.Fprintf(&encoded, "\\%03o", char)
	}
	// The builtin marker can precede readline's terminal restoration. Wait
	// for the next prompt instead, without matching echoed command input.
	initial := "stty size; printf '__INITIAL__\\n'; PS1=$(printf '" + encoded.String() + "')\n"
	if strings.Contains(initial, readyPrompt) {
		t.Fatal("readiness prompt was present in echoed input")
	}
	if _, err := term.Write([]byte(initial)); err != nil {
		t.Fatal(err)
	}
	output.await(t, "__INITIAL__\r\n")
	output.await(t, "24 80")
	output.await(t, readyPrompt)
	if err := term.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("stty size; printf '__RESIZED__\\n'; exit 7\n")); err != nil {
		t.Fatal(err)
	}
	output.await(t, "40 100")
	if code := waitTerminal(t, term); code != 7 {
		t.Fatalf("exit code %d", code)
	}
	select {
	case <-output.done:
	case <-time.After(time.Second):
		t.Fatal("terminal output did not drain after process exit")
	}
	if !strings.Contains(output.text(), "__RESIZED__") {
		t.Fatal("last output lost")
	}
}
func TestTerminalUnixCtrlC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term, err := StartTerminal(ctx, TerminalOptions{Shell: "sh", Command: "trap 'printf __INTERRUPTED__; exit 9' INT; printf __RUNNING__; while :; do sleep 1; done", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	out := captureTerminal(t, term)
	out.await(t, "__RUNNING__")
	if _, err := term.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	out.await(t, "__INTERRUPTED__")
	if code := waitTerminal(t, term); code != 9 {
		t.Fatal(code)
	}
}
func TestTerminalUnixSignalAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	term, err := StartTerminal(ctx, TerminalOptions{Shell: "sh", Command: "printf __READY__; sleep 30", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	out := captureTerminal(t, term)
	out.await(t, "__READY__")
	if err := term.Signal("INVALID"); err == nil {
		t.Fatal("unknown signal accepted")
	}
	if err := term.Signal("TERM"); err != nil {
		t.Fatal(err)
	}
	if code := waitTerminal(t, term); code != 143 {
		t.Fatalf("signal exit code %d", code)
	}
	cancel()
	ctx2, cancel2 := context.WithCancel(context.Background())
	term2, err := StartTerminal(ctx2, TerminalOptions{Shell: "sh", Command: "printf __READY__; sleep 30", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer term2.Close()
	out2 := captureTerminal(t, term2)
	out2.await(t, "__READY__")
	cancel2()
	if code := waitTerminal(t, term2); code != 137 {
		t.Fatal(code)
	}
	select {
	case <-out2.done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release terminal reader")
	}
}
func TestTerminalUnixModesEnvironmentAndCommand(t *testing.T) {
	term, err := StartTerminal(context.Background(), TerminalOptions{Shell: "sh", Command: "printf 'TERM=%s ENV=%s\\n' \"$TERM\" \"$SHELLPIER_PTY_ENV\"; stty -a", Term: "vt100", Env: []string{"SHELLPIER_PTY_ENV=present"}, Columns: 80, Rows: 24, Modes: map[uint8]uint32{53: 0, 51: 1, 50: 1, 36: 1, 72: 1, 1: 3, 5: 4, 159: 123}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	out := captureTerminal(t, term)
	out.await(t, "TERM=vt100 ENV=present")
	waitTerminal(t, term)
	out.await(t, "-echo")
}
func TestTerminalUnixCloseResizeRace(t *testing.T) {
	term, err := StartTerminal(context.Background(), TerminalOptions{Shell: "sh", Command: "sleep 30", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	captureTerminal(t, term)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				_ = term.Resize(80, 24)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = term.Close() }()
	wg.Wait()
	_ = term.Close()
	waitTerminal(t, term)
}

func TestTerminalUnixBackgroundJobsCancelled(t *testing.T) {
	unrelated := exec.Command("sleep", "30")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term, err := StartTerminal(ctx, TerminalOptions{Shell: "sh", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	output := captureTerminal(t, term)
	command := `set -m; sleep 30 & printf '__BACKGROUND_PID:%s__\n' $! ; printf '__BACKGROUND_READY__\n'` + "\n"
	_, err = term.Write([]byte(command))
	if err != nil {
		t.Fatal(err)
	}
	output.await(t, "__BACKGROUND_READY__\r\n")
	match := regexp.MustCompile(`__BACKGROUND_PID:([0-9]+)__`).FindStringSubmatch(output.text())
	if len(match) != 2 {
		t.Fatalf("missing background PID: %q", output.text())
	}
	pid, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if group == term.(*unixTerminal).cmd.Process.Pid {
		t.Fatal("background job did not get a separate job-control group")
	}
	cancel()
	waitTerminal(t, term)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ordinary background job survived terminal cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(unrelated.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated process was affected: %v", err)
	}
}

func TestTerminalUnixResizeKernelWhileWaitingForInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Non-interactive read has no prompt/readline transition that can write
	// its cached old winsize over the caller's resize.
	terminal, err := StartTerminal(ctx, TerminalOptions{Shell: "sh", Command: "printf '__AWAIT_INPUT__\\n'; read answer; stty size; exit 7", Term: "xterm", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	output := captureTerminal(t, terminal)
	output.await(t, "__AWAIT_INPUT__\r\n")
	if err = terminal.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	size, err := pty.GetsizeFull(terminal.(*unixTerminal).file)
	if err != nil {
		t.Fatal(err)
	}
	if size.Cols != 100 || size.Rows != 40 {
		t.Fatal("kernel did not accept the requested terminal dimensions")
	}
	if _, err = terminal.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	output.await(t, "40 100")
	if code := waitTerminal(t, terminal); code != 7 {
		t.Fatalf("exit code %d", code)
	}
}
