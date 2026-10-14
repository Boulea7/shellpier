package sshd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/crypto/ssh"
)

func request(t *testing.T, s *ssh.Session, name string, payload any) bool {
	t.Helper()
	var encoded []byte
	if payload != nil {
		encoded = ssh.Marshal(payload)
	}
	ok, err := s.SendRequest(name, true, encoded)
	if err != nil {
		t.Fatalf("%s request: %v", name, err)
	}
	return ok
}
func TestAuthenticatedCapabilities(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	ok, raw, err := c.SendRequest("capabilities@shellpier", true, nil)
	if err != nil || !ok {
		t.Fatalf("capabilities: %v", err)
	}
	var rawFields map[string]any
	if json.Unmarshal(raw, &rawFields) != nil || rawFields["os"] != runtime.GOOS || rawFields["arch"] != runtime.GOARCH {
		t.Fatal("capabilities did not use the documented JSON fields")
	}
	var value capabilities
	if json.Unmarshal(raw, &value) != nil || value.Protocol != 1 || value.OS != runtime.GOOS || value.Arch != runtime.GOARCH || !value.Exec || !value.SFTP || value.PTY != platform.TerminalSupported() {
		t.Fatal("capabilities mismatch")
	}
	if bytes.Contains(raw, []byte("private")) || bytes.Contains(raw, []byte("work_dir")) {
		t.Fatal("capabilities leaked local configuration")
	}
	ok, _, err = c.SendRequest("unknown-global", true, nil)
	if err != nil || ok {
		t.Fatal("unknown global request accepted")
	}
	ok, _, err = c.SendRequest("capabilities@shellpier", true, []byte("unexpected"))
	if err != nil || ok {
		t.Fatal("capabilities accepted unexpected payload")
	}
}
func TestSessionEnvironmentAndWorkingDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	dir := t.TempDir()
	subdir := filepath.Join(dir, "中文 directory")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	c, _ := client(t, ctx, key, key.PublicKey(), dir)
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !request(t, s, "env", struct{ Name, Value string }{"SHELLPIER_TEST_VALUE", "中文 value"}) {
		t.Fatal("valid environment rejected")
	}
	if request(t, s, "env", struct{ Name, Value string }{"A=B", "invalid"}) || request(t, s, "env", struct{ Name, Value string }{"NUL", "bad\x00"}) {
		t.Fatal("invalid environment accepted")
	}
	if !request(t, s, "cwd@shellpier", struct{ Directory string }{filepath.Base(subdir)}) {
		t.Fatal("valid cwd rejected")
	}
	if request(t, s, "cwd@shellpier", struct{ Directory string }{filepath.Join(dir, "missing")}) {
		t.Fatal("missing cwd accepted")
	}
	if request(t, s, "shell", nil) {
		t.Fatal("shell without a PTY accepted")
	}
	command := "printf '%s\\n' \"$SHELLPIER_TEST_VALUE\"; pwd"
	if runtime.GOOS == "windows" {
		command = "[Console]::WriteLine($env:SHELLPIER_TEST_VALUE); [Console]::WriteLine((Get-Location).Path)"
	}
	out, err := s.Output(command)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), "\n")
	if len(lines) != 2 || lines[0] != "中文 value" {
		t.Fatal("environment value was not applied exactly")
	}
	expected, err := os.Stat(subdir)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.Stat(lines[1])
	if err != nil || !os.SameFile(expected, actual) {
		t.Fatal("working directory did not refer to the requested directory")
	}

}
func TestSessionBoundedAndPostStartRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < MaxEnvironmentKeys; i++ {
		if err = s.Setenv("ENV_"+strings.Repeat("X", i), "value"); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Setenv("OVER_LIMIT", "value"); err == nil {
		t.Fatal("environment count limit ignored")
	}
	if request(t, s, "exec", struct{ Command string }{strings.Repeat("a", MaxCommandBytes+1)}) {
		t.Fatal("oversized command accepted")
	}
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	if err = s.Start(command); err != nil {
		t.Fatal(err)
	}
	if request(t, s, "env", struct{ Name, Value string }{"POSTSTART", "value"}) || request(t, s, "cwd@shellpier", struct{ Directory string }{t.TempDir()}) || request(t, s, "shell", nil) || request(t, s, "exec", struct{ Command string }{"whoami"}) || request(t, s, "signal", struct{ Name string }{"TERM"}) || request(t, s, "unknown", nil) {
		t.Fatal("unsupported post-start request accepted")
	}
	s.Close()
}

type terminalOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *terminalOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *terminalOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }
func waitTerminalOutput(t *testing.T, b *terminalOutput, value string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for {
		if strings.Contains(b.String(), value) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal did not produce %q", value)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestPTYInteractiveResizeAndSignal(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("native terminal unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if request(t, s, "pty-req", struct {
		Term                         string
		Columns, Rows, Width, Height uint32
		Modes                        string
	}{"xterm", 80, 24, 0, 0, "\x00"}) {
		t.Fatal("second PTY request accepted")
	}
	output := &terminalOutput{}
	s.Stdout = output
	s.Stderr = output
	stdin, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	if !request(t, s, "window-change", struct{ Columns, Rows, Width, Height uint32 }{100, 30, 0, 0}) {
		t.Fatal("post-start resize rejected")
	}
	if !request(t, s, "window-change", struct{ Columns, Rows, Width, Height uint32 }{0, 0, 0, 0}) {
		t.Fatal("zero resize should preserve current dimensions")
	}
	if request(t, s, "window-change", struct{ Columns, Rows, Width, Height uint32 }{1001, 0, 0, 0}) {
		t.Fatal("oversized resize accepted")
	}
	command := "printf '__interactive_ok__\\n'; stty size\n"
	if runtime.GOOS == "windows" {
		command = "[Console]::WriteLine('__interactive_ok__')\r\n"
	}
	if _, err = io.WriteString(stdin, command); err != nil {
		t.Fatal(err)
	}
	waitTerminalOutput(t, output, "__interactive_ok__")
	if runtime.GOOS != "windows" {
		waitTerminalOutput(t, output, "30 100")
	}
	if !request(t, s, "signal", struct{ Name string }{"KILL"}) {
		t.Fatal("terminal KILL rejected")
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("terminal did not exit after KILL")
	}
}
func TestPTYExecAndClose(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("native terminal unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, done := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	command := "printf 'pty-exec-ok'; exit 7"
	if runtime.GOOS == "windows" {
		command = "[Console]::Write('pty-exec-ok'); exit 7"
	}
	out, err := s.CombinedOutput(command)
	exit, ok := err.(*ssh.ExitError)
	if !ok || exit.ExitStatus() != 7 || !strings.Contains(string(out), "pty-exec-ok") {
		t.Fatalf("PTY exec output/status mismatch: %q %v", out, err)
	}
	s, err = c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("terminal blocked connection cancellation")
	}
}

func TestPTYEOFAndSessionTTL(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("native terminal unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, serverDone := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	output := &terminalOutput{}
	s.Stdout = output
	s.Stderr = output
	input, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	command := "printf '__eof_output__\\n'; exit 0\n"
	if runtime.GOOS == "windows" {
		command = "[Console]::WriteLine('__eof_output__'); exit 0\r\n"
	}
	if _, err = io.WriteString(input, command); err != nil {
		t.Fatal(err)
	}
	input.Close()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stdin EOF killed shell before normal exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shell did not exit after stdin EOF and exit command")
	}
	if !strings.Contains(output.String(), "__eof_output__") {
		t.Fatal("final terminal output was lost")
	}
	s, err = c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(4 * time.Second):
		t.Fatal("context cancellation left terminal running")
	}
}
func TestPTYRequestPayloadValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, value := range []struct {
		Term                         string
		Columns, Rows, Width, Height uint32
		Modes                        string
	}{
		{"xterm", 1001, 24, 0, 0, "\x00"}, {"xterm", 80, 1001, 0, 0, "\x00"}, {"bad\nterm", 80, 24, 0, 0, "\x00"}, {"xterm", 80, 24, 0, 0, "\x35\x00"}, {"xterm", 80, 24, 0, 0, "\x00trailing"},
	} {
		if request(t, s, "pty-req", value) {
			t.Fatal("malformed terminal request accepted")
		}
	}
	if platform.TerminalSupported() {
		if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
			t.Fatal("invalid requests consumed PTY slot")
		}
	}
}
func TestTerminalModesEncoding(t *testing.T) {
	encoded := string([]byte{53, 0, 0, 0, 0, 51, 0, 0, 0, 1, 0})
	if !validTerminalModes(encoded) {
		t.Fatal("valid mode encoding rejected")
	}
	values := terminalModes(encoded)
	if values[53] != 0 || values[51] != 1 || len(values) != 2 {
		t.Fatal("terminal modes parsed incorrectly")
	}
	encoded = string([]byte{53, 0, 0, 0, 1, 160, 1, 2})
	if !validTerminalModes(encoded) || len(terminalModes(encoded)) != 1 {
		t.Fatal("RFC reserved opcode did not stop mode processing")
	}
}
func TestConcurrentPTYAndExecSessions(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("native terminal unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	failures := make(chan error, 4)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func(pty bool) {
			defer workers.Done()
			s, err := c.NewSession()
			if err != nil {
				failures <- err
				return
			}
			defer s.Close()
			if pty {
				if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
					failures <- err
					return
				}
			}
			command := "printf 'concurrent-ok'"
			if runtime.GOOS == "windows" {
				command = "[Console]::Write('concurrent-ok')"
			}
			out, err := s.CombinedOutput(command)
			if err != nil {
				failures <- err
				return
			}
			if !strings.Contains(string(out), "concurrent-ok") {
				failures <- io.ErrUnexpectedEOF
			}
		}(i%2 == 0)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("concurrent session: %v", err)
	}
}

func TestPTYUnixEchoMode(t *testing.T) {
	if runtime.GOOS == "windows" || !platform.TerminalSupported() {
		t.Skip("Unix termios mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
		t.Fatal(err)
	}
	output := &terminalOutput{}
	s.Stdout = output
	s.Stderr = output
	input, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(input, "printf '__noecho_output__\\n'; exit 0\n")
	input.Close()
	if err = s.Wait(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "__noecho_output__") || strings.Contains(text, "printf '__noecho_output__") {
		t.Fatal("SSH ECHO mode was not applied")
	}
}
func TestPTYUnixBackgroundJobCancellation(t *testing.T) {
	if runtime.GOOS == "windows" || !platform.TerminalSupported() {
		t.Skip("Unix shell job control")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	dir := t.TempDir()
	c, done := client(t, ctx, key, key.PublicKey(), dir)
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	input, err := s.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output := &terminalOutput{}
	s.Stdout = output
	s.Stderr = output
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(input, "(trap '' HUP; sleep 1; printf escaped > escaped) & echo $! > child.pid\n")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(filepath.Join(dir, "child.pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background job did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("background job blocked cancellation")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err = os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(err) {
		t.Fatal("normal background job survived terminal cancellation")
	}
}
func TestPTYContextDeadline(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("native terminal unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := signer(t)
	c, done := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Shell(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if ctx.Err() == nil {
			t.Fatal("session terminated before the deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TTL did not end terminal session")
	}
}

func TestEnvironmentMergePreservesWindowsDriveEntries(t *testing.T) {
	values := mergeEnvironment([]string{"=C:=C:\\work", "=D:=D:\\other", "VAR=old"}, []string{"VAR=new"})
	entries := map[string]bool{}
	for _, entry := range values {
		entries[entry] = true
	}
	if !entries["=C:=C:\\work"] || !entries["=D:=D:\\other"] || !entries["VAR=new"] || len(values) != 3 {
		t.Fatal("environment merge lost drive entries or override")
	}
}

func TestOpenSSHForcedPTYWithoutLocalTTY(t *testing.T) {
	if runtime.GOOS == "windows" || !platform.TerminalSupported() {
		t.Skip("Unix OpenSSH subprocess")
	}
	if _, err := os.Stat("/usr/bin/ssh"); err != nil {
		t.Skip("system OpenSSH unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	host := signer(t)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	user, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	if err = os.WriteFile(identity, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(dir, "known_hosts")
	keyLine := fmt.Sprintf("[127.0.0.1]:%s %s", port, ssh.MarshalAuthorizedKey(host.PublicKey()))
	if err = os.WriteFile(known, []byte(keyLine), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "ssh_config")
	settings := fmt.Sprintf("Host isolated\n HostName 127.0.0.1\n Port %s\n User shellpier\n IdentityFile %q\n UserKnownHostsFile %q\n GlobalKnownHostsFile /dev/null\n StrictHostKeyChecking yes\n IdentitiesOnly yes\n IdentityAgent none\n BatchMode yes\n PasswordAuthentication no\n KbdInteractiveAuthentication no\n ConnectTimeout 3\n", port, identity, known)
	if err = os.WriteFile(config, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- (&Server{Signer: host, AuthorizedKey: user.PublicKey(), Shell: "sh", WorkDir: dir}).ServeConn(ctx, conn)
	}()
	// os/exec uses pipes, deliberately providing no controlling local terminal.
	command := exec.CommandContext(ctx, "/usr/bin/ssh", "-F", config, "-tt", "isolated", "stty size; printf '__openssh_pty_ok__\\n'")
	command.Stdin = strings.NewReader("")
	command.Env = mergeEnvironment(os.Environ(), []string{"TERM=xterm-256color"})
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "24 80") || !strings.Contains(string(output), "__openssh_pty_ok__") {
		t.Fatalf("OpenSSH forced PTY from a pipe failed: %v; allocation rejected=%t", err, bytes.Contains(output, []byte("PTY allocation request failed")))
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenSSH test server did not close")
	}
}

func TestPTYZeroDimensionProtocol(t *testing.T) {
	if runtime.GOOS == "windows" || !platform.TerminalSupported() {
		t.Skip("Unix stty dimension inspection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RequestPty("xterm", 0, 0, ssh.TerminalModes{}); err != nil {
		t.Fatal("zero initial dimensions rejected")
	}
	if !request(t, s, "window-change", struct{ Columns, Rows, Width, Height uint32 }{120, 0, 0, 0}) {
		t.Fatal("zero pre-start row rejected")
	}
	output := &terminalOutput{}
	s.Stdout = output
	s.Stderr = output
	if err = s.Start("sleep 0.2; stty size"); err != nil {
		t.Fatal(err)
	}
	if !request(t, s, "window-change", struct{ Columns, Rows, Width, Height uint32 }{0, 40, 0, 0}) {
		t.Fatal("zero post-start column rejected")
	}
	if !request(t, s, "window-change", struct{ Columns, Rows, Width, Height uint32 }{0, 0, 0, 0}) {
		t.Fatal("zero resize rejected")
	}
	if err = s.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "40 120") {
		t.Fatal("zero resize did not preserve the existing dimension")
	}
}
func TestPTYZeroDimensionNormalization(t *testing.T) {
	for _, value := range []struct{ columns, rows, currentColumns, currentRows, wantColumns, wantRows uint32 }{
		{0, 0, 80, 24, 80, 24}, {0, 30, 80, 24, 80, 30}, {120, 0, 80, 24, 120, 24}, {0, 0, 120, 40, 120, 40},
	} {
		columns, rows := terminalDimensions(value.columns, value.rows, value.currentColumns, value.currentRows)
		if columns != value.wantColumns || rows != value.wantRows {
			t.Fatal("zero dimension normalization mismatch")
		}
		if !validDimensions(value.columns, value.rows) {
			t.Fatal("valid informational zero dimension rejected")
		}
	}
	if validDimensions(MaxTerminalDimension+1, 0) || validDimensions(0, MaxTerminalDimension+1) {
		t.Fatal("dimension size limit was lost")
	}
}
