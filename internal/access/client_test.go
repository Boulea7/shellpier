package access

import (
	"context"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/sshd"
)

func serveTestSSH(t *testing.T, host Ticket) string {
	t.Helper()
	signer, e := privateKey(host.HostPrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := publicKey(host.ClientKey)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	// This fixture tests SSH routing and authentication, not PowerShell startup.
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "cmd"
	}
	server := &sshd.Server{Signer: signer, AuthorizedKey: pub, Shell: shell, WorkDir: t.TempDir()}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go func() { _ = server.ServeConn(ctx, c) }()
		}
	}()
	return ln.Addr().String()
}
func TestDialPinAuthenticationAndFallback(t *testing.T) {
	a, h, e := NewPair(time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	_, wrong, e := NewPair(time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	wrong.ClientKey = h.ClientKey
	wrongAddr := serveTestSSH(t, wrong)
	rightAddr := serveTestSSH(t, h)
	a.Addresses = []string{wrongAddr, rightAddr}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, e := Dial(ctx, a, "")
	if e != nil {
		t.Fatal(e)
	}
	session, e := client.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	out, e := session.CombinedOutput("echo access-ok")
	session.Close()
	if e != nil || strings.TrimSpace(string(out)) != "access-ok" {
		t.Fatalf("exec failed: %v (context: %v)", e, ctx.Err())
	}
	client.Close()
	a.Addresses = []string{wrongAddr}
	rejectionCtx, rejectionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rejectionCancel()
	if c, e := Dial(rejectionCtx, a, ""); e == nil {
		c.Close()
		t.Fatal("host key mismatch accepted")
	}
	if rejectionCtx.Err() != nil {
		t.Fatal("host key rejection was masked by a test deadline")
	}
	a.Addresses = []string{rightAddr}
	other, _, _ := NewPair(time.Minute)
	a.PrivateKey = other.PrivateKey
	authCtx, authCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer authCancel()
	if c, e := Dial(authCtx, a, ""); e == nil {
		c.Close()
		t.Fatal("unauthorized client accepted")
	}
	if authCtx.Err() != nil {
		t.Fatal("authentication rejection was masked by a test deadline")
	}
}
func TestDialCancellationClosesClient(t *testing.T) {
	a, h, _ := NewPair(time.Minute)
	a.Addresses = []string{serveTestSSH(t, h)}
	ctx, cancel := context.WithCancel(context.Background())
	client, e := Dial(ctx, a, "")
	if e != nil {
		cancel()
		t.Fatal(e)
	}
	cancel()
	done := make(chan struct{})
	go func() { client.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		client.Close()
		t.Fatal("cancellation did not close SSH")
	}
}
