package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/relay"
	"golang.org/x/crypto/ssh"
)

func connectionFixture(t *testing.T, wss bool, drop bool) (Ticket, *atomic.Int32) {
	t.Helper()
	ticket, host, err := NewPair(0)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(host.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	config.AddHostKey(signer)
	ctx, cancel := context.WithCancel(context.Background())
	count := &atomic.Int32{}
	var workers sync.WaitGroup
	serve := func(raw net.Conn) {
		defer workers.Done()
		defer raw.Close()
		stop := context.AfterFunc(ctx, func() { raw.Close() })
		defer stop()
		server, channels, requests, err := ssh.NewServerConn(raw, config)
		if err != nil {
			return
		}
		defer server.Close()
		go func() {
			for request := range requests {
				if request.Type == "keepalive@openssh.com" {
					count.Add(1)
					if drop {
						continue
					}
				}
				request.Reply(false, nil)
			}
		}()
		for incoming := range channels {
			channel, requests, err := incoming.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					if request.Type != "exec" {
						request.Reply(false, nil)
						continue
					}
					request.Reply(true, nil)
					var command struct{ Command string }
					_ = ssh.Unmarshal(request.Payload, &command)
					if command.Command == "stream" {
						for i := 0; i < 80; i++ {
							if _, err := channel.Write([]byte("busy\n")); err != nil {
								return
							}
							select {
							case <-ctx.Done():
								return
							case <-time.After(5 * time.Millisecond):
							}
						}
					}
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
					return
				}
			}()
		}
	}
	done := make(chan struct{})
	if wss {
		key := strings.Repeat("k", 32)
		server, err := relay.New(key, 8)
		if err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewServer(server)
		session, err := relay.Create(ctx, httpServer.URL, key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		ticket.ID, ticket.RelayURL, ticket.RelayToken = session.ID, httpServer.URL, session.ClientToken
		go func() {
			defer close(done)
			raw, err := relay.DialHost(ctx, httpServer.URL, session)
			if err == nil {
				workers.Add(1)
				serve(raw)
			}
		}()
		t.Cleanup(func() { cancel(); server.Close(); httpServer.Close(); <-done; workers.Wait() })
	} else {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ticket.Addresses = []string{listener.Addr().String()}
		go func() {
			defer close(done)
			for {
				raw, err := listener.Accept()
				if err != nil {
					return
				}
				workers.Add(1)
				go serve(raw)
			}
		}()
		t.Cleanup(func() { cancel(); listener.Close(); <-done; workers.Wait() })
	}
	return ticket, count
}
func fastConnectionOptions() ConnectionOptions {
	return ConnectionOptions{ConnectTimeout: time.Second, KeepaliveInterval: 10 * time.Millisecond, KeepaliveTimeout: 60 * time.Millisecond}
}
func waitConnection(t *testing.T, client *ssh.Client) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- client.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		client.Close()
		t.Fatal("connection did not terminate")
		return nil
	}
}

func TestConnectionKeepaliveBlackholeAndFalseReply(t *testing.T) {
	for _, wss := range []bool{false, true} {
		for _, drop := range []bool{false, true} {
			t.Run(fmt.Sprint(wss, drop), func(t *testing.T) {
				ticket, count := connectionFixture(t, wss, drop)
				client, err := DialWithOptions(context.Background(), ticket, "", fastConnectionOptions())
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				if drop {
					if err = waitConnection(t, client); !errors.Is(err, ErrKeepaliveTimeout) {
						t.Fatalf("timeout cause lost: %v", err)
					}
					if got := count.Load(); got != 1 {
						t.Fatalf("outstanding keepalive count %d", got)
					}
					return
				}
				time.Sleep(140 * time.Millisecond)
				session, err := client.NewSession()
				if err != nil {
					t.Fatal("false reply incorrectly killed client", err)
				}
				defer session.Close()
				if err = session.Run("noop"); err != nil {
					t.Fatal(err)
				}
				if count.Load() < 2 {
					t.Fatal("keepalive requests not sent")
				}
			})
		}
	}
}
func TestConnectionBusyAndSetupDeadlineDoesNotKillSession(t *testing.T) {
	for _, wss := range []bool{false, true} {
		t.Run(fmt.Sprint(wss), func(t *testing.T) {
			ticket, _ := connectionFixture(t, wss, false)
			options := fastConnectionOptions()
			options.ConnectTimeout = 250 * time.Millisecond
			ctx := WithConnectionOptions(context.Background(), options)
			client, err := Dial(ctx, ticket, "")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			session.Stdout = io.Discard
			if err = session.Run("stream"); err != nil {
				t.Fatal("setup deadline escaped into session or busy connection died", err)
			}
		})
	}
}
func TestConnectionDisableCancelAndTTL(t *testing.T) {
	ticket, count := connectionFixture(t, false, true)
	options := fastConnectionOptions()
	options.KeepaliveInterval = 0
	client, err := DialWithOptions(context.Background(), ticket, "", options)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatal("disabled keepalive sent requests")
	}
	client.Close()
	for _, expiry := range []bool{false, true} {
		current := ticket
		ctx, cancel := context.WithCancel(context.Background())
		options = fastConnectionOptions()
		options.KeepaliveTimeout = time.Second
		if expiry {
			current.ExpiresAt = time.Now().Add(120 * time.Millisecond)
		}
		client, err = DialWithOptions(ctx, current, "", options)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if !expiry {
			cancel()
		}
		if err = waitConnection(t, client); errors.Is(err, ErrKeepaliveTimeout) {
			t.Fatalf("TTL/cancel misclassified: %v", err)
		}
		cancel()
		client.Close()
	}
}
func TestConnectionNoKeepaliveGoroutineLeak(t *testing.T) {
	stackCount := func() int {
		data := make([]byte, 1<<20)
		n := runtime.Stack(data, true)
		return strings.Count(string(data[:n]), "access.startKeepalive")
	}
	baseline := stackCount()
	ticket, _ := connectionFixture(t, false, true)
	for i := 0; i < 8; i++ {
		client, err := DialWithOptions(context.Background(), ticket, "", fastConnectionOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(waitConnection(t, client), ErrKeepaliveTimeout) {
			t.Fatal("missing keepalive timeout")
		}
		client.Close()
	}
	deadline := time.Now().Add(time.Second)
	for stackCount() > baseline {
		if time.Now().After(deadline) {
			t.Fatal("keepalive worker/request leaked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestConnectionOptionsValidationAndHandshakeBudget(t *testing.T) {
	ticket := directTicket(t)
	options := DefaultConnectionOptions()
	options.ConnectTimeout = 0
	if _, err := DialWithOptions(context.Background(), ticket, "", options); err == nil {
		t.Fatal("invalid options reached network")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			raw <- conn
		}
	}()
	ticket.Addresses = []string{listener.Addr().String()}
	options = fastConnectionOptions()
	options.ConnectTimeout = 30 * time.Millisecond
	started := time.Now()
	_, err = DialWithOptions(context.Background(), ticket, "", options)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("handshake not bounded: %v", err)
	}
	select {
	case conn := <-raw:
		conn.Close()
	case <-time.After(time.Second):
		t.Fatal("not accepted")
	}
}

func TestConnectionProxyEnforcesTicketExpiry(t *testing.T) {
	ticket, count := connectionFixture(t, false, false)
	ticket.ExpiresAt = time.Now().Add(180 * time.Millisecond)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	options := fastConnectionOptions()
	options.KeepaliveInterval = 0
	ctx := WithConnectionOptions(context.Background(), options)
	err := Proxy(ctx, ticket, "", input, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("proxy did not enforce ticket expiry: %v", err)
	}
	if count.Load() != 0 {
		t.Fatal("raw proxy stream ran SSH keepalives")
	}
}
