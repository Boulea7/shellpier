//go:build windows

package platform

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTerminalWindowsRedirectedParentHandles(t *testing.T) {
	const helperEnv = "SHELLPIER_TEST_CONPTY_REDIRECTED_PARENT"
	const marker = "__CONPTY_REDIRECTED_OUTPUT__"
	if os.Getenv(helperEnv) == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		terminal, err := StartTerminal(ctx, TerminalOptions{Shell: "cmd", Command: "echo " + marker + " & exit /b 0", Columns: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		defer terminal.Close()
		output, err := io.ReadAll(terminal)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(output), marker) {
			t.Fatal("command output bypassed the ConPTY stream")
		}
		if code := waitTerminal(t, terminal); code != 0 {
			t.Fatalf("exit code %d", code)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalWindowsRedirectedParentHandles$")
	command.Env = append(os.Environ(), helperEnv+"=1")
	command.Stdin = strings.NewReader("")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		t.Fatalf("redirected parent regression: %v: %s", err, output.String())
	}
	if strings.Contains(output.String(), marker) {
		t.Fatal("ConPTY command leaked into its parent's standard output")
	}
}

func TestTerminalWindowsInteractiveResizeAndExit(t *testing.T) {
	if !TerminalSupported() {
		t.Fatal("Windows CI requires ConPTY (Windows 10 1809+)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	term, err := StartTerminal(ctx, TerminalOptions{Shell: "cmd", Columns: 80, Rows: 24, Term: "xterm"})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	out := captureTerminal(t, term)
	if err := term.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := term.Write([]byte("echo __CONPTY_READY__\r\nexit /b 7\r\n")); err != nil {
		t.Fatal(err)
	}
	out.await(t, "__CONPTY_READY__")
	if code := waitTerminal(t, term); code != 7 {
		t.Fatalf("exit code %d", code)
	}
	select {
	case <-out.done:
	case <-time.After(3 * time.Second):
		t.Fatal("ConPTY output did not drain")
	}
	if err := term.Signal("USR1"); err == nil {
		t.Fatal("unsupported Unix signal accepted")
	}
}
func TestTerminalWindowsCancelAndRepeatedClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	term, err := StartTerminal(ctx, TerminalOptions{Shell: "cmd", Command: "echo __RUNNING__ & ping -n 60 127.0.0.1 >nul", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	out := captureTerminal(t, term)
	out.await(t, "__RUNNING__")
	cancel()
	waitTerminal(t, term)
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalWindowsCloseWithoutOutputConsumer(t *testing.T) {
	term, err := StartTerminal(context.Background(), TerminalOptions{Shell: "cmd", Command: "for /l %i in (1,1,20000) do @echo __OUTPUT_BACKPRESSURE__", Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- term.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("ConPTY close deadlocked without an output consumer")
	}
	waitTerminal(t, term)
}
