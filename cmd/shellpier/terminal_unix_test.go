//go:build linux || darwin

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestBinaryTerminalRestoreAndResize(t *testing.T) {
	binary, ticket := agentBinary(t), agentEndpoint(t)
	for _, tc := range []struct {
		name    string
		code    int
		command string
	}{
		{"success", 0, "printf 'READY\\n'; read line; exit 0"},
		{"remote-error", 7, "printf 'READY\\n'; read line; exit 7"},
		{"cancel-resize", 130, "printf 'READY\\n'; while :; do stty size; sleep 0.05; done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err = pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, binary, "shell", "--retries", "0", "--command", tc.command, ticket)
			// Only the private slave is passed to the client; the test runner's terminal is untouched.
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill() }()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			lines := make(chan string, 64)
			go func() {
				defer close(lines)
				reader := bufio.NewReader(master)
				for {
					line, e := reader.ReadString('\n')
					if line != "" {
						select {
						case lines <- line:
						case <-ctx.Done():
							return
						}
					}
					if e != nil {
						return
					}
				}
			}()
			waitLine := func(want string) {
				t.Helper()
				for {
					select {
					case line, ok := <-lines:
						if !ok {
							t.Fatalf("PTY closed waiting for %q", want)
						}
						if strings.Contains(line, want) {
							return
						}
					case <-ctx.Done():
						t.Fatalf("waiting for %q: %v", want, ctx.Err())
					}
				}
			}
			waitLine("READY")
			raw, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(before, raw) {
				t.Fatal("local terminal never entered raw mode")
			}
			if tc.name == "cancel-resize" {
				if err = pty.Setsize(slave, &pty.Winsize{Rows: 38, Cols: 101}); err != nil {
					t.Fatal(err)
				}
				waitLine("38 101")
				if err = cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = fmt.Fprintln(master, "finish"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if tc.code == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				agentExit(t, err, tc.code)
			}
			after, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("terminal state was not restored")
			}
		})
	}
}

func TestAgentBinaryJSONCancellation(t *testing.T) {
	binary, ticket := agentBinary(t), agentEndpoint(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Pipe a readiness marker through non-JSON stdout is not possible with captured exec.
	// Signal after the SSH server has processed a separately observable filesystem effect.
	marker := t.TempDir() + "/ready"
	cmd := exec.CommandContext(ctx, binary, "exec", "--json", "--retries", "0", ticket, "--", "printf ready > '"+marker+"'; sleep 30")
	outPath := t.TempDir() + "/stdout"
	errPath := t.TempDir() + "/stderr"
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	diagnostic, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}
	defer diagnostic.Close()
	cmd.Stdout, cmd.Stderr = out, diagnostic
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err = cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	agentExit(t, waitErr, 130)
	resultData, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	agentSingleJSON(t, resultData)
	data, err := os.ReadFile(errPath)
	if err != nil {
		t.Fatal(err)
	}
	envelope := agentSingleJSON(t, data)
	detail, ok := envelope["error"].(map[string]any)
	if !ok || detail["code"] != "canceled" {
		t.Fatalf("diagnostic: %#v", envelope)
	}
}
