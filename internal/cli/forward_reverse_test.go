package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func reverseTicket(t *testing.T, parent context.Context, lifetime time.Duration, mode sshd.AccessMode) string {
	t.Helper()
	a, h, err := access.NewPair(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &sshd.Server{Signer: signer, AuthorizedKey: public, Shell: "unused", WorkDir: t.TempDir(), Reverse: sshd.NewReverseForwardLimits(), AccessMode: mode}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err == nil {
			_ = server.ServeConn(ctx, raw)
		}
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("reverse fixture leaked")
		}
	})
	a.Addresses = []string{listener.Addr().String()}
	data, _ := json.Marshal(a)
	p := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	return "@" + p
}

type reverseEvent struct {
	Direction     string `json:"direction"`
	RemoteAddress string `json:"remote_address"`
	LocalTarget   string `json:"local_target"`
	LocalAddress  string `json:"local_address"`
	Target        string `json:"target"`
}
type reverseEventWriter struct{ ready chan reverseEvent }

func (w reverseEventWriter) Write(data []byte) (int, error) {
	var event reverseEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	w.ready <- event
	return len(data), nil
}
func startReverse(t *testing.T, ctx context.Context, ticket, target string) (reverseEvent, <-chan error) {
	t.Helper()
	writer := reverseEventWriter{make(chan reverseEvent, 1)}
	done := make(chan error, 1)
	go func() {
		done <- forward(ctx, []string{ticket, "--reverse", "--listen", "0", "--to", target, "--json"}, writer, io.Discard)
	}()
	select {
	case event := <-writer.ready:
		return event, done
	case err := <-done:
		t.Fatalf("reverse failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("reverse did not become ready")
	}
	return reverseEvent{}, done
}
func assertReverseStopped(t *testing.T, done <-chan error, address string) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil {
			t.Error("missing termination cause")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reverse stayed alive")
	}
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("remote listener survived shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReverseForwardHTTPAndCancellation(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "reverse "+r.URL.Path) }))
	defer backend.Close()
	target := strings.TrimPrefix(backend.URL, "http://")
	ticket := reverseTicket(t, context.Background(), time.Minute, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event, done := startReverse(t, ctx, ticket, target)
	if event.Direction != "reverse" || event.LocalTarget != target || event.Target != target || event.LocalAddress != "" || strings.HasSuffix(event.RemoteAddress, ":0") {
		t.Fatalf("wrong event: %+v", event)
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := client.Get("http://" + event.RemoteAddress + "/hello")
			if err != nil {
				t.Error(err)
				return
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(data) != "reverse /hello" {
				t.Errorf("data=%q err=%v", data, err)
			}
		}()
	}
	wg.Wait()
	cancel()
	assertReverseStopped(t, done, event.RemoteAddress)
}

func TestReverseForwardExpiryAndTransportLoss(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "transport"}[loss], func(t *testing.T) {
			hostCtx, hostCancel := context.WithCancel(context.Background())
			defer hostCancel()
			lifetime := time.Minute
			if !loss {
				lifetime = time.Second
			}
			ticket := reverseTicket(t, hostCtx, lifetime, "")
			event, done := startReverse(t, context.Background(), ticket, "localhost:12345")
			if loss {
				hostCancel()
			}
			assertReverseStopped(t, done, event.RemoteAddress)
		})
	}
}
func TestReverseForwardNoFalseReadiness(t *testing.T) {
	ticket := reverseTicket(t, context.Background(), time.Minute, "sftp")
	var out bytes.Buffer
	err := forward(context.Background(), []string{ticket, "--reverse", "--to", "localhost:12345", "--json"}, &out, io.Discard)
	if err == nil || out.Len() != 0 {
		t.Fatalf("rejected listener announced ready: %s %v", out.String(), err)
	}
}
func TestReverseForwardCancelActiveConnection(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := backend.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ticket := reverseTicket(t, context.Background(), time.Minute, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event, done := startReverse(t, ctx, ticket, backend.Addr().String())
	conn, err := net.Dial("tcp", event.RemoteAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var local net.Conn
	select {
	case local = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("local target not dialed")
	}
	defer local.Close()
	cancel()
	assertReverseStopped(t, done, event.RemoteAddress)
	local.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	_, err = local.Read(buf[:])
	if err == nil {
		t.Fatal("local target survived cancellation")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("local target connection leaked")
	}
}

func TestReverseForwardConcurrentLimit(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 2*sshd.MaxSessions)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	defer func() {
		backend.Close()
		<-acceptDone
		close(accepted)
		for conn := range accepted {
			conn.Close()
		}
	}()
	ticket := reverseTicket(t, context.Background(), time.Minute, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event, done := startReverse(t, ctx, ticket, backend.Addr().String())
	for i := 0; i < sshd.MaxSessions+4; i++ {
		conn, err := net.DialTimeout("tcp", event.RemoteAddress, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	count := 0
wait:
	for {
		select {
		case conn := <-accepted:
			defer conn.Close()
			count++
		case <-timer.C:
			break wait
		}
	}
	if count < 1 || count > sshd.MaxSessions {
		t.Fatalf("unexpected local stream count: %d", count)
	}
	cancel()
	assertReverseStopped(t, done, event.RemoteAddress)
}
