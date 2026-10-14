package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func executionHost(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("SHELLPIER_RELAY_URL", "")
	directory := t.TempDir()
	ticket := filepath.Join(directory, "access.json")
	startHost(t, "share", "--relay=", "--listen", "127.0.0.1:0", "--ttl", "1m", "--work-dir", directory, "--output", ticket, "--json")
	return "@" + ticket, directory
}

func TestExecExactCommandJSONAndExit(t *testing.T) {
	ticket, _ := executionHost(t)
	command := "printf 'a b $literal'; printf 'diagnostic' >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "[Console]::Write('a b $literal'); [Console]::Error.Write('diagnostic'); exit 7"
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{"--json", "exec", ticket, "--command", command}, nil, &stdout, &stderr, "test")
	var remote *ssh.ExitError
	if !errors.As(err, &remote) || remote.ExitStatus() != 7 {
		t.Fatalf("remote exit was lost: %v", err)
	}
	var result execResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.ExitCode != 7 || result.Stdout != "a b $literal" || result.Stderr != "diagnostic" || result.StdoutEncoding != "utf-8" {
		t.Fatalf("unexpected result: %+v", result)
	}
	var diagnostic struct {
		Error struct {
			Code           string `json:"code"`
			RemoteExitCode int    `json:"remote_exit_code"`
		} `json:"error"`
	}
	if json.Unmarshal(stderr.Bytes(), &diagnostic) != nil || diagnostic.Error.Code != "remote_exit" || diagnostic.Error.RemoteExitCode != 7 || strings.Contains(stderr.String(), "$literal") {
		t.Fatalf("invalid safe error: %s", stderr.String())
	}
}

func TestExecEnvironmentWorkdirScriptAndStdin(t *testing.T) {
	ticket, directory := executionHost(t)
	working := filepath.Join(directory, "工作 目录")
	if err := os.Mkdir(working, 0700); err != nil {
		t.Fatal(err)
	}
	command := "printf '%s\\n' \"$SHELLPIER_TEST\"; pwd; cat"
	if runtime.GOOS == "windows" {
		command = "[Console]::WriteLine($env:SHELLPIER_TEST); [Console]::WriteLine((Get-Location).Path); [Console]::Write([Console]::In.ReadToEnd())"
	}
	script := filepath.Join(directory, "script.txt")
	if err := os.WriteFile(script, []byte(command), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"exec", "--script", script, ticket, "--cwd", working, "--env", "SHELLPIER_TEST=space $value", "--timeout", "15s"}, strings.NewReader("input 中文"), &stdout, io.Discard, "test")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.ReplaceAll(stdout.String(), "\r\n", "\n"), "\n", 3)
	if len(parts) != 3 || parts[0] != "space $value" || parts[2] != "input 中文" {
		t.Fatalf("environment or stdin changed: %q", stdout.String())
	}
	expected, err := os.Stat(working)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.Stat(parts[1])
	if err != nil || !os.SameFile(expected, actual) {
		t.Fatalf("remote working directory differs: %q, %v", parts[1], err)
	}
}

func TestExecCaptureBoundAndBinary(t *testing.T) {
	ticket, _ := executionHost(t)
	command := "printf '\\377abcdef'"
	if runtime.GOOS == "windows" {
		command = "[Console]::OpenStandardOutput().Write([byte[]](255,97,98,99,100,101,102),0,7)"
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"exec", ticket, "--json", "--max-output", "3", "--command", command}, nil, &stdout, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	var result execResult
	if json.Unmarshal(stdout.Bytes(), &result) != nil || !result.OK || result.Stdout != "/2Fi" || result.StdoutEncoding != "base64" || !result.StdoutTruncated {
		t.Fatalf("capture was not bounded and lossless: %s", stdout.String())
	}
}

func TestExecTimeoutAndArguments(t *testing.T) {
	ticket, _ := executionHost(t)
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	start := time.Now()
	err := Run(context.Background(), []string{"exec", ticket, "--timeout", "500ms", "--command", command}, nil, io.Discard, io.Discard, "test")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout not honored: %v", err)
	}
	for _, args := range [][]string{
		{"exec", "@missing"},
		{"exec", "@missing", "--command", "one", "--", "two"},
		{"exec", "@missing", "--env", "BAD-NAME=value", "--command", "one"},
		{"exec", "@missing", "--max-output", "16777217", "--command", "one"},
	} {
		if err := Run(context.Background(), args, nil, io.Discard, io.Discard, "test"); err == nil {
			t.Fatalf("invalid arguments accepted: %s", args[0])
		}
	}
}

func TestCapabilitiesAndShellPTY(t *testing.T) {
	ticket, _ := executionHost(t)
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"capabilities", ticket, "--json"}, nil, &output, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	var capabilities remoteCapabilities
	if json.Unmarshal(output.Bytes(), &capabilities) != nil || capabilities.OS != runtime.GOOS || !capabilities.Exec || !capabilities.SFTP || !capabilities.CWD || !capabilities.Env {
		t.Fatalf("invalid target capabilities: %s", output.String())
	}
	if !capabilities.PTY {
		t.Skip("target OS has no PTY support")
	}
	if len(capabilities.PTYSignals) == 0 || runtime.GOOS == "windows" && strings.Join(capabilities.PTYSignals, ",") != "INT,TERM,KILL" {
		t.Fatalf("terminal signals mismatch: %v", capabilities.PTYSignals)
	}
	output.Reset()
	command := "printf 'terminal-ready'; exit 7"
	if runtime.GOOS == "windows" {
		command = "[Console]::Write('terminal-ready'); exit 7"
	}
	err := Run(context.Background(), []string{"shell", ticket, "--no-raw", "--cols", "91", "--rows", "33", "--command", command, "--timeout", "15s"}, strings.NewReader(""), &output, io.Discard, "test")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 7 || !strings.Contains(output.String(), "terminal-ready") {
		t.Fatalf("PTY shell command failed: %v, %q", err, output.String())
	}
}
