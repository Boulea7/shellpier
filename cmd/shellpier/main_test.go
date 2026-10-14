package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func TestBinaryExitCodes(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "shellpier")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), time.Minute)
	defer cancelBuild()
	if output, err := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
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
	defer listener.Close()
	ticket.Addresses = []string{listener.Addr().String()}
	shell, command := "sh", "exit 7"
	if runtime.GOOS == "windows" {
		shell, command = "cmd", "exit /b 7"
	}
	server := &sshd.Server{Signer: signer, AuthorizedKey: key, Shell: shell}
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = server.ServeConn(ctx, conn)
		}
		served <- err
	}()
	encoded, err := ticket.Encode()
	if err != nil {
		t.Fatal(err)
	}
	assertExit := func(expected int) {
		t.Helper()
		output, err := exec.CommandContext(ctx, binary, "connect", "--retries", "0", encoded, "--", command).CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("client timed out: %v\n%s", ctx.Err(), output)
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != expected {
			t.Fatalf("want process exit %d, got %v\n%s", expected, err, output)
		}
	}
	assertExit(7)
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("serve SSH: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("SSH server did not stop")
	}
	// Keep the address reserved while refusing the SSH handshake, avoiding a
	// closed-port race with another local process.
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
		served <- err
	}()
	assertExit(1)
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("failed connection did not finish")
	}
}
