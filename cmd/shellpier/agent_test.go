package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func agentBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shellpier")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", path, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return path
}

func agentEndpoint(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	ticket, host, err := access.NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(host.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(host.ClientKey))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "cmd"
	}
	server := &sshd.Server{Signer: signer, AuthorizedKey: key, Shell: shell, WorkDir: t.TempDir()}
	done := make(chan struct{})
	var connections sync.WaitGroup
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			connections.Add(1)
			go func() { defer connections.Done(); _ = server.ServeConn(ctx, conn) }()
		}
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("endpoint listener did not close")
		}
		drained := make(chan struct{})
		go func() { connections.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Error("SSH sessions did not close")
		}
	})
	ticket.Addresses = []string{listener.Addr().String()}
	encoded, err := ticket.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.json")
	if err = os.WriteFile(path, []byte(encoded), 0600); err != nil {
		t.Fatal(err)
	}
	return "@" + path
}

func agentSingleJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("invalid JSON: %v: %q", err, data)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra output after JSON: %v: %q", err, data)
	}
	return result
}

func agentExit(t *testing.T, err error, want int) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != want {
		t.Fatalf("exit: got %v, want %d", err, want)
	}
}

func TestAgentBinaryJSONExitAndUsage(t *testing.T) {
	binary, ticket := agentBinary(t), agentEndpoint(t)
	command := "printf stdout-marker; printf stderr-marker >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "echo stdout-marker & echo stderr-marker 1>&2 & exit /b 7"
	}
	for _, tc := range []struct {
		name       string
		args       []string
		code       int
		diagnostic string
	}{
		{"remote", []string{"exec", "--json", "--retries", "0", ticket, "--", command}, 7, "remote_exit"},
		{"usage", []string{"exec", "--json", "--not-a-real-flag"}, 2, "usage"},
		{"missing-ticket", []string{"exec", "--json"}, 2, "usage"},
		{"missing-value", []string{"keygen", "--json", "--out"}, 2, "usage"},
		{"timeout", []string{"exec", "--json", "--retries", "0", "--timeout", "200ms", ticket, "--", agentWaitCommand()}, 124, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, tc.args...)
			cmd.Dir = t.TempDir()
			var out, diagnostic bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &diagnostic
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			agentExit(t, err, tc.code)
			envelope := agentSingleJSON(t, diagnostic.Bytes())
			detail, ok := envelope["error"].(map[string]any)
			if !ok || detail["code"] != tc.diagnostic {
				t.Fatalf("diagnostic: %#v", envelope)
			}
			if tc.diagnostic == "usage" && out.Len() != 0 {
				t.Fatalf("usage wrote stdout: %q", out.Bytes())
			}
			if tc.name == "timeout" {
				agentSingleJSON(t, out.Bytes())
			}
			if tc.name == "remote" {
				result := agentSingleJSON(t, out.Bytes())
				if result["exit_code"] != float64(7) || !bytes.Contains([]byte(result["stdout"].(string)), []byte("stdout-marker")) || !bytes.Contains([]byte(result["stderr"].(string)), []byte("stderr-marker")) {
					t.Fatalf("result: %#v", result)
				}
			}
		})
	}
}

func agentWaitCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 30 127.0.0.1 >nul"
	}
	return "sleep 30"
}
