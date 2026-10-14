package platform

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTerminalValidation(t *testing.T) {
	for _, o := range []TerminalOptions{{Columns: 0, Rows: 24}, {Columns: 80, Rows: 32768}, {Columns: 80, Rows: 24, Term: strings.Repeat("t", 129)}, {Columns: 80, Rows: 24, Command: "bad\x00command"}, {Columns: 80, Rows: 24, Env: []string{"invalid"}}} {
		if terminal, err := StartTerminal(context.Background(), o); err == nil {
			terminal.Close()
			t.Fatalf("invalid options accepted: %#v", o)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StartTerminal(ctx, TerminalOptions{Columns: 80, Rows: 24}); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestTerminalEnvironmentMerge(t *testing.T) {
	t.Setenv("SHELLPIER_PTY_ENV", "old")
	env, err := terminalEnvironment(TerminalOptions{Term: "xterm", Env: []string{"SHELLPIER_PTY_ENV=new", "TERM=ignored"}})
	if err != nil {
		t.Fatal(err)
	}
	foundEnv, foundTerm, base := false, false, false
	for _, entry := range env {
		if entry == "SHELLPIER_PTY_ENV=new" {
			foundEnv = true
		}
		if entry == "TERM=xterm" {
			foundTerm = true
		}
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") {
			base = true
		}
	}
	if !foundEnv || !foundTerm || !base {
		t.Fatal("environment overrides lost base environment or TERM")
	}
}

type terminalCapture struct {
	mu      sync.Mutex
	output  strings.Builder
	changed chan struct{}
	done    chan struct{}
}

func captureTerminal(t *testing.T, terminal Terminal) *terminalCapture {
	t.Helper()
	c := &terminalCapture{changed: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				c.mu.Lock()
				c.output.Write(buffer[:n])
				c.mu.Unlock()
				select {
				case c.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}
func (c *terminalCapture) text() string { c.mu.Lock(); defer c.mu.Unlock(); return c.output.String() }
func (c *terminalCapture) await(t *testing.T, value string) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		if strings.Contains(c.text(), value) {
			return
		}
		select {
		case <-c.changed:
		case <-c.done:
			if !strings.Contains(c.text(), value) {
				t.Fatalf("terminal ended before %q; output %q", value, c.text())
			}
			return
		case <-deadline.C:
			t.Fatalf("terminal did not emit %q; output %q", value, c.text())
		}
	}
}
func waitTerminal(t *testing.T, terminal Terminal) uint32 {
	t.Helper()
	type result struct {
		code uint32
		err  error
	}
	done := make(chan result, 1)
	go func() { code, err := terminal.Wait(); done <- result{code, err} }()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.code
	case <-time.After(8 * time.Second):
		terminal.Close()
		t.Fatal("terminal process did not exit")
	}
	return 0
}

var _ io.ReadWriteCloser = (Terminal)(nil)
