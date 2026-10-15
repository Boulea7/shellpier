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

// A real authenticated SSH transport isolates forwarding from host CLI dispatch.
func forwardTicket(t *testing.T, parent context.Context, targets []string, lifetime time.Duration) string {
	t.Helper()
	a, h, err := access.NewPair(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := sshd.NewForwardPolicy(targets)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.Addresses = []string{listener.Addr().String()}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		defer stop()
		config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return nil, errors.New("denied")
			}
			return nil, nil
		}}
		config.AddHostKey(host)
		connection, channels, requests, err := ssh.NewServerConn(raw, config)
		if err != nil {
			return
		}
		defer connection.Close()
		go ssh.DiscardRequests(requests)
		local, stopLocal := context.WithCancel(ctx)
		defer stopLocal()
		var workers sync.WaitGroup
		for incoming := range channels {
			workers.Add(1)
			go func() { defer workers.Done(); _ = policy.Serve(local, incoming) }()
		}
		stopLocal()
		workers.Wait()
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SSH forwarding fixture leaked")
		}
	})
	path := filepath.Join(t.TempDir(), "access.json")
	data, _ := json.Marshal(a)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return "@" + path
}

type forwardEventWriter struct{ ready chan string }

func (w forwardEventWriter) Write(data []byte) (int, error) {
	var event struct {
		Local   string `json:"local_address"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	if event.Command != "forward" {
		return 0, errors.New("unexpected event")
	}
	w.ready <- event.Local
	return len(data), nil
}
func startForward(t *testing.T, ctx context.Context, ticket, target string) (string, <-chan error) {
	t.Helper()
	output := forwardEventWriter{make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- forward(ctx, []string{ticket, "--to", target, "--json"}, output, io.Discard) }()
	select {
	case address := <-output.ready:
		return address, done
	case err := <-done:
		t.Fatalf("forward did not start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("forward startup timed out")
	}
	return "", done
}
func TestForwardListenerValidation(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.1:80", ":0", "local.example:0", "127.1:0", "[::1%zone]:0", "127.0.0.1:+1"} {
		if _, err := forwardListenerAddress(address); err == nil {
			t.Errorf("accepted non-loopback or ambiguous listener %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "[::1]:0", "localhost:8080", "[::ffff:127.0.0.1]:0", "08080", "127.0.0.1:080"} {
		if _, err := forwardListenerAddress(address); err != nil {
			t.Errorf("valid loopback rejected: %s %v", address, err)
		}
	}
	if err := forward(context.Background(), []string{"@missing", "--listen", "0.0.0.0:0", "--to", "127.0.0.1:80"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("listener was not rejected before ticket IO: %v", err)
	}
}
func TestForwardCLIHTTPAndCancel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "through SSH") }))
	defer backend.Close()
	target := strings.TrimPrefix(backend.URL, "http://")
	hostCtx, hostCancel := context.WithCancel(context.Background())
	defer hostCancel()
	ticket := forwardTicket(t, hostCtx, []string{target}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address, done := startForward(t, ctx, ticket, target)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != "through SSH" {
		t.Fatalf("HTTP result %q %v", data, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel cause lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel left forwarding alive")
	}
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("listener survived cancellation")
	}
}
func TestForwardCLIRefusalAndTransportLoss(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "transport_loss"}[loss], func(t *testing.T) {
			hostCtx, hostCancel := context.WithCancel(context.Background())
			defer hostCancel()
			ticket := forwardTicket(t, hostCtx, []string{"127.0.0.1:1"}, time.Minute)
			address, done := startForward(t, context.Background(), ticket, "127.0.0.1:80")
			if loss {
				hostCancel()
			} else {
				conn, err := net.Dial("tcp", address)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_, _ = conn.Write([]byte("request"))
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failure was not reported")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("transport failure did not close listener")
			}
			if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
				conn.Close()
				t.Fatal("failed forwarding listener remained open")
			}
		})
	}
}
func TestForwardCLITicketExpiry(t *testing.T) {
	hostCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticket := forwardTicket(t, hostCtx, nil, time.Second)
	address, done := startForward(t, context.Background(), ticket, "127.0.0.1:80")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ticket expiry was not reported")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ticket expiry left listener open")
	}
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("expired listener remained open")
	}
}
