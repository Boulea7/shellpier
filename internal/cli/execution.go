package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

type environmentValues []string

func (values *environmentValues) String() string { return "" }

func (values *environmentValues) Set(value string) error {
	name, content, ok := strings.Cut(value, "=")
	total := len(value)
	for _, entry := range *values {
		total += len(entry)
	}
	if !ok || name == "" || len(name) > 64 || len(content) > 4096 || len(*values) >= 32 || total > 16*1024 || strings.ContainsRune(value, 0) {
		return errors.New("env needs NAME=VALUE, at most 32 entries and 16 KiB total; names 1..64 bytes and values at most 4096 bytes")
	}
	for i, char := range name {
		if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || i > 0 && char >= '0' && char <= '9') {
			return errors.New("env name must contain letters, digits, or underscore and cannot start with a digit")
		}
	}
	*values = append(*values, value)
	return nil
}

func configureSession(session *ssh.Session, cwd string, env []string) error {
	if cwd != "" {
		if len(cwd) > 4096 || strings.ContainsRune(cwd, 0) {
			return errors.New("cwd must be at most 4096 bytes without NUL")
		}
		ok, err := session.SendRequest("cwd@shellpier", true, ssh.Marshal(struct{ Directory string }{cwd}))
		if err != nil {
			return fmt.Errorf("set remote cwd: %w", err)
		}
		if !ok {
			return errors.New("target does not support the requested working directory")
		}
	}
	for _, value := range env {
		name, content, _ := strings.Cut(value, "=")
		if err := session.Setenv(name, content); err != nil {
			return fmt.Errorf("target rejected environment variable %s", name)
		}
	}
	return nil
}

func remoteCommand(mode, exact, script string, args []string) (string, error) {
	forms := 0
	for _, used := range []bool{exact != "", script != "", len(args) != 0} {
		if used {
			forms++
		}
	}
	if forms > 1 {
		return "", errors.New("choose --command, --script, or a command after --")
	}
	command := exact
	if script != "" {
		data, err := readRegular(script, 64*1024)
		if err != nil {
			return "", fmt.Errorf("read command script: %w", err)
		}
		command = string(data)
	} else if len(args) != 0 {
		command = strings.Join(args, " ")
	}
	if command == "" && mode == "connect" {
		command = "whoami"
	}
	if command == "" || len(command) > 64*1024 || strings.ContainsRune(command, 0) || !utf8.ValidString(command) {
		return "", errors.New("provide a UTF-8 shell command of 1..65536 bytes without NUL")
	}
	return command, nil
}

type limitedCapture struct {
	data      []byte
	limit     int
	truncated bool
}

func (w *limitedCapture) Write(p []byte) (int, error) {
	remaining := w.limit - len(w.data)
	if len(p) > remaining {
		w.data = append(w.data, p[:remaining]...)
		w.truncated = true
	} else {
		w.data = append(w.data, p...)
	}
	return len(p), nil
}

// executionOutcome separates an observed remote exit from transport uncertainty.
func executionOutcome(err error) (string, bool) {
	if err == nil {
		return "exited", true
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return "exited", true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "expired", false
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", false
	}
	return "connection_lost", false
}

type execResult struct {
	TerminationReason string `json:"termination_reason"`
	ExitStatusKnown   bool   `json:"exit_status_known"`
	SchemaVersion     int    `json:"schema_version"`
	Command           string `json:"command"`
	OK                bool   `json:"ok"`
	ExitCode          int    `json:"exit_code"`
	Stdout            string `json:"stdout"`
	StdoutEncoding    string `json:"stdout_encoding"`
	Stderr            string `json:"stderr"`
	StderrEncoding    string `json:"stderr_encoding"`
	StdoutTruncated   bool   `json:"stdout_truncated"`
	StderrTruncated   bool   `json:"stderr_truncated"`
}

func executionResult(command string, err error, stdout, stderr *limitedCapture) execResult {
	code := 0
	if err != nil {
		code = -1
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitStatus()
		}
	}
	encode := func(data []byte) (string, string) {
		if utf8.Valid(data) {
			return string(data), "utf-8"
		}
		return base64.StdEncoding.EncodeToString(data), "base64"
	}
	out, outEncoding := encode(stdout.data)
	diagnostic, diagnosticEncoding := encode(stderr.data)
	reason, known := executionOutcome(err)
	return execResult{reason, known, 1, command, err == nil, code, out, outEncoding, diagnostic, diagnosticEncoding, stdout.truncated, stderr.truncated}
}
