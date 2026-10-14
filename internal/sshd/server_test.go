package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s, e := ssh.NewSignerFromKey(k)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func client(t *testing.T, ctx context.Context, key ssh.Signer, auth ssh.PublicKey, dir string) (*ssh.Client, <-chan error) {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ln.Close() })
	host := signer(t)
	done := make(chan error, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			done <- e
			return
		}
		done <- (&Server{Signer: host, AuthorizedKey: auth, WorkDir: dir}).ServeConn(ctx, c)
	}()
	c, e := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "ignored-user", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 5 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c, done
}
func TestExecAndSFTP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	dir := t.TempDir()
	c, _ := client(t, ctx, key, key.PublicKey(), dir)
	sess, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer sess.Close()
	cmd := "printf '中文'; printf 'oops' >&2; exit 7"
	if runtime.GOOS == "windows" {
		cmd = "[Console]::Write('中文'); [Console]::Error.Write('oops'); exit 7"
	}
	out, e := sess.CombinedOutput(cmd)
	var exit *ssh.ExitError
	if !errors.As(e, &exit) || exit.ExitStatus() != 7 {
		t.Fatalf("exit: %v", e)
	}
	if !strings.Contains(string(out), "中文") || !strings.Contains(string(out), "oops") {
		t.Fatalf("output %q", out)
	}
	sf, e := sftp.NewClient(c)
	if e != nil {
		t.Fatal(e)
	}
	defer sf.Close()
	cwd, e := sf.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	name := path.Join(cwd, "中文.txt")
	if runtime.GOOS == "windows" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	f, e := sf.Create("中文.txt")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte("上传下载")); e != nil {
		t.Fatal(e)
	}
	f.Close()
	f, e = sf.Open(name)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	f.Close()
	if e != nil || string(data) != "上传下载" {
		t.Fatalf("download %q %v", data, e)
	}
	if actual, e := os.ReadFile(filepath.Join(dir, "中文.txt")); e != nil || string(actual) != "上传下载" {
		t.Fatal("SFTP paths did not resolve to the configured work directory")
	}
}
func TestRejectKey(t *testing.T) {
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	host, auth, bad := signer(t), signer(t), signer(t)
	go func() {
		c, e := ln.Accept()
		if e == nil {
			(&Server{Signer: host, AuthorizedKey: auth.PublicKey()}).ServeConn(context.Background(), c)
		}
	}()
	c, e := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "irrelevant", Auth: []ssh.AuthMethod{ssh.PublicKeys(bad)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 3 * time.Second})
	if e == nil {
		c.Close()
		t.Fatal("wrong key accepted")
	}
}
func TestTTLStopsCommandTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-tree marker command")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	dir := t.TempDir()
	c, done := client(t, ctx, key, key.PublicKey(), dir)
	s, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// The descendant would create a marker after cancellation if it escaped the group.
	if e = s.Start("(sleep 1; touch escaped) & echo ready; wait"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(e) {
		t.Fatalf("descendant survived: %v", e)
	}
}
func TestStdinAndPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix stdin shell command")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, _ := client(t, ctx, key, key.PublicKey(), t.TempDir())
	s, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); e == nil {
		t.Fatal("PTY accepted")
	}
	s.Stdin = strings.NewReader("input中文")
	out, e := s.Output("cat")
	if e != nil || string(out) != "input中文" {
		t.Fatalf("stdin %q: %v", out, e)
	}
}

func TestSessionCloseStopsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix marker command")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	dir := t.TempDir()
	c, _ := client(t, ctx, key, key.PublicKey(), dir)
	s, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Start("sleep 1; touch escaped"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	s.Close()
	time.Sleep(1100 * time.Millisecond)
	if _, e = os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(e) {
		t.Fatalf("closed session process survived: %v", e)
	}
	// The same transport remains usable after stopping the closed channel's process.
	s, e = c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, e := s.Output("printf alive")
	if e != nil || string(out) != "alive" {
		t.Fatalf("transport: %q %v", out, e)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	server, peer := net.Pipe()
	defer peer.Close()
	key := signer(t)
	done := make(chan error, 1)
	go func() {
		done <- (&Server{Signer: key, AuthorizedKey: key.PublicKey(), HandshakeTimeout: 50 * time.Millisecond}).ServeConn(context.Background(), server)
	}()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("silent handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("silent connection did not expire")
	}
}

func TestSFTPCancelAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	c, done := client(t, ctx, key, key.PublicKey(), t.TempDir())
	sf, e := sftp.NewClient(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = sf.Close(); e != nil {
		t.Fatal(e)
	}
	// Closing an SFTP channel frees its session slot and keeps the transport usable.
	sf, e = sftp.NewClient(c)
	if e != nil {
		t.Fatal(e)
	}
	defer sf.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SFTP prevented transport shutdown")
	}
}

func TestHandshakeDeadlineCleared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sleep command")
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	key, host := signer(t), signer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		conn, e := ln.Accept()
		if e == nil {
			(&Server{Signer: host, AuthorizedKey: key.PublicKey(), HandshakeTimeout: 200 * time.Millisecond}).ServeConn(ctx, conn)
		}
	}()
	c, e := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "unused", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	s, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	out, e := s.Output("sleep 0.35; printf alive")
	if e != nil || string(out) != "alive" {
		t.Fatalf("authenticated session still has handshake deadline: %q %v", out, e)
	}
}

func TestWindowsTTLStopsJobChild(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows job-object integration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := signer(t)
	dir := t.TempDir()
	c, done := client(t, ctx, key, key.PublicKey(), dir)
	session, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	ready := filepath.Join(dir, "child-ready")
	escaped := filepath.Join(dir, "escaped")
	pidFile := filepath.Join(dir, "child.pid")
	childCommand := "[System.IO.File]::WriteAllText(" + quote(ready) + ", 'ready'); Start-Sleep -Seconds 4; [System.IO.File]::WriteAllText(" + quote(escaped) + ", 'escaped')"
	words := utf16.Encode([]rune(childCommand))
	raw := make([]byte, len(words)*2)
	for i, w := range words {
		binary.LittleEndian.PutUint16(raw[i*2:], w)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	command := "$child = Start-Process -FilePath powershell.exe -NoNewWindow -PassThru -ArgumentList '-NoProfile','-NonInteractive','-EncodedCommand'," + quote(encoded) + "; Set-Content -LiteralPath " + quote(pidFile) + " -Value $child.Id -Encoding ASCII; Wait-Process -Id $child.Id"
	if e = session.Start(command); e != nil {
		t.Fatal("could not start managed Windows child")
	}
	deadline := time.Now().Add(12 * time.Second)
	var child *os.Process
	for {
		if child == nil {
			if data, e := os.ReadFile(pidFile); e == nil {
				pid, e := strconv.Atoi(strings.TrimSpace(string(data)))
				if e != nil {
					t.Fatal("invalid child PID")
				}
				child, e = os.FindProcess(pid)
				if e != nil {
					t.Fatal(e)
				}
				defer child.Release()
				defer child.Kill()
			}
		}
		if _, e := os.Stat(ready); e == nil && child != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Windows child did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Hold the child process handle so cleanup targets this process even if its PID is reused.
	childDone := make(chan struct{})
	go func() { child.Wait(); close(childDone) }()
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("Windows SSH server did not stop")
	}
	select {
	case <-childDone:
	case <-time.After(2 * time.Second):
		t.Fatal("job close did not terminate the spawned child")
	}
	if _, e := os.Stat(escaped); !os.IsNotExist(e) {
		t.Fatal("Windows job child survived cancellation")
	}
}
