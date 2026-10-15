package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type multiEventWriter struct{ ready chan []localForward }

func (w multiEventWriter) Write(data []byte) (int, error) {
	var event struct {
		Event     string         `json:"event"`
		Direction string         `json:"direction"`
		Forwards  []localForward `json:"forwards"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	w.ready <- event.Forwards
	return len(data), nil
}
func startMulti(t *testing.T, ctx context.Context, ticket string, targets ...string) ([]localForward, <-chan error) {
	t.Helper()
	w := multiEventWriter{make(chan []localForward, 1)}
	done := make(chan error, 1)
	args := []string{ticket, "--json"}
	for _, target := range targets {
		args = append(args, "--local", "0="+target)
	}
	go func() { done <- forward(ctx, args, w, io.Discard) }()
	select {
	case event := <-w.ready:
		return event, done
	case err := <-done:
		t.Fatalf("start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("start timeout")
	}
	return nil, done
}
func echoForwardBackend(t *testing.T, address string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l
}
func forwardEcho(t *testing.T, address string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte("binary\x00"))
	b := make([]byte, 7)
	if _, err = io.ReadFull(c, b); err != nil || string(b) != "binary\x00" {
		t.Fatalf("echo %q %v", b, err)
	}
}
func TestForwardMultipleRecoveryAndSingleSSH(t *testing.T) {
	backend := echoForwardBackend(t, "127.0.0.1:0")
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	recoverTarget := reserved.Addr().String()
	reserved.Close()
	// This fixture accepts exactly one authenticated SSH transport.
	ticket := forwardTicket(t, context.Background(), []string{backend.Addr().String(), recoverTarget}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mappings, done := startMulti(t, ctx, ticket, backend.Addr().String(), recoverTarget)
	if len(mappings) != 2 || mappings[0].Target != backend.Addr().String() {
		t.Fatalf("metadata %v", mappings)
	}
	healthy, err := net.Dial("tcp", mappings[0].Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Close()
	_ = healthy.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = healthy.Write([]byte("before"))
	check := make([]byte, 6)
	if _, err = io.ReadFull(healthy, check); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", mappings[1].Listen)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = c.Read(b[:]); err == nil {
		t.Fatal("unavailable target survived")
	}
	c.Close()
	_, _ = healthy.Write([]byte("after!"))
	if _, err = io.ReadFull(healthy, check); err != nil || string(check) != "after!" {
		t.Fatalf("healthy stream interrupted: %q %v", check, err)
	}
	forwardEcho(t, mappings[0].Listen)
	echoForwardBackend(t, recoverTarget)
	forwardEcho(t, mappings[1].Listen)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("multi cancellation leaked")
	}
	for _, m := range mappings {
		if c, err := net.DialTimeout("tcp", m.Listen, 100*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("listener survived")
		}
	}
}
func TestForwardFixedAndReverseRecoverSamePort(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "reverse"}[reverse], func(t *testing.T) {
			reserved, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			target := reserved.Addr().String()
			reserved.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var address string
			var done <-chan error
			if reverse {
				event, finished := startReverse(t, ctx, reverseTicket(t, context.Background(), time.Minute, ""), target)
				address = event.RemoteAddress
				done = finished
			} else {
				address, done = startForward(t, ctx, forwardTicket(t, context.Background(), []string{target}, time.Minute), target)
			}
			c, err := net.Dial("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err = c.Read(b[:]); err == nil {
				t.Fatal("unavailable target survived")
			}
			c.Close()
			echoForwardBackend(t, target)
			forwardEcho(t, address)
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation leaked")
			}
		})
	}
}
func TestForwardMultipleBindRollback(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := first.Addr().String()
	first.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	ticket := forwardTicket(t, context.Background(), []string{"localhost:80"}, time.Minute)
	var out bytes.Buffer
	err = forward(context.Background(), []string{ticket, "--local", address + "=localhost:80", "--local", occupied.Addr().String() + "=localhost:80", "--json"}, &out, io.Discard)
	if err == nil || out.Len() != 0 {
		t.Fatalf("bind failure %v %s", err, out.String())
	}
	again, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("first listener leaked: %v", err)
	}
	again.Close()
}
func TestForwardMultipleFlagBoundaries(t *testing.T) {
	for _, flag := range []string{"--listen=0", "--to=localhost:80", "--dynamic=false", "--reverse=false"} {
		if err := forward(context.Background(), []string{"@missing", "--local", "0=localhost:80", flag}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("conflict %s: %v", flag, err)
		}
	}
	var flags localForwardFlags
	if err := flags.Set("[::1]:0=[::1]:80"); err != nil {
		t.Fatal(err)
	}
	for len(flags) < 16 {
		if err := flags.Set("0=localhost:80"); err != nil {
			t.Fatal(err)
		}
	}
	if err := flags.Set("0=localhost:80"); err == nil {
		t.Fatal("accepted >16")
	}
}

func TestForwardMultipleExpiryAndTransportLoss(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "transport"}[loss], func(t *testing.T) {
			hostCtx, hostCancel := context.WithCancel(context.Background())
			defer hostCancel()
			lifetime := time.Minute
			if !loss {
				lifetime = time.Second
			}
			ticket := forwardTicket(t, hostCtx, []string{"localhost:80"}, lifetime)
			mappings, done := startMulti(t, context.Background(), ticket, "localhost:80", "localhost:80")
			if loss {
				hostCancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("missing termination error")
				}
				if !loss && (!errors.Is(err, context.DeadlineExceeded) || classifyJSONError(err).Code != "timeout") {
					t.Fatalf("TTL cause lost: %v", err)
				}
				if loss && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
					t.Fatalf("transport misclassified: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("listeners outlived transport/TTL")
			}
			for _, mapping := range mappings {
				if c, err := net.DialTimeout("tcp", mapping.Listen, 100*time.Millisecond); err == nil {
					c.Close()
					t.Fatal("listener survived")
				}
			}
		})
	}
}

func TestForwardMultipleContextCause(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "timeout"}[deadline], func(t *testing.T) {
			ticket := forwardTicket(t, context.Background(), []string{"localhost:80"}, time.Minute)
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			_, done := startMulti(t, ctx, ticket, "localhost:80")
			if !deadline {
				cancel()
			}
			want := context.Canceled
			code := "canceled"
			if deadline {
				want = context.DeadlineExceeded
				code = "timeout"
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) || classifyJSONError(err).Code != code {
					t.Fatalf("cause %v classified %s", err, classifyJSONError(err).Code)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup timeout")
			}
		})
	}
}

func TestPendingForwardDialHoldsAdmissionUntilSSHCompletes(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport_close", true: "late_accept"}[late], func(t *testing.T) { testPendingForwardDial(t, late) })
	}
}
func testPendingForwardDial(t *testing.T, late bool) {
	_, host, err := access.NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(host.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var opened atomic.Int32
	release := make(chan struct{})
	lateClosed := make(chan error, sshd.MaxSessions)
	ready := make(chan *ssh.ServerConn, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		raw, e := listener.Accept()
		if e != nil {
			return
		}
		defer raw.Close()
		config := &ssh.ServerConfig{NoClientAuth: true}
		config.AddHostKey(signer)
		server, channels, requests, e := ssh.NewServerConn(raw, config)
		if e != nil {
			return
		}
		defer server.Close()
		ready <- server
		go func() {
			for request := range requests {
				if request.WantReply {
					_ = request.Reply(false, nil)
				}
			}
		}()
		for incoming := range channels {
			opened.Add(1)
			go func(incoming ssh.NewChannel) {
				<-release
				channel, requests, err := incoming.Accept()
				if err != nil {
					lateClosed <- err
					return
				}
				defer channel.Close()
				go ssh.DiscardRequests(requests)
				var b [1]byte
				_, err = channel.Read(b[:])
				lateClosed <- err
			}(incoming)
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	<-ready
	slots := make(chan struct{}, sshd.MaxSessions)
	var workers sync.WaitGroup
	var peers []net.Conn
	for round := 0; round < 3; round++ {
		for i := 0; i < sshd.MaxSessions; i++ {
			select {
			case slots <- struct{}{}:
				local, peer := net.Pipe()
				peers = append(peers, peer)
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer func() { <-slots }()
					defer local.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer cancel()
					_, _ = pendingForwardDial(ctx, client, local, "tcp", "localhost:80")
				}()
			default:
			}
		}
		time.Sleep(60 * time.Millisecond)
	}
	// The peer is responsive to global keepalives but intentionally never answers
	// channel-open. Timed-out local streams must not release their shared slots.
	if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != sshd.MaxSessions || len(slots) != sshd.MaxSessions {
		t.Fatalf("unbounded pending opens: %d, slots %d", opened.Load(), len(slots))
	}
	for _, peer := range peers {
		_ = peer.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, err := peer.Read(b[:]); err == nil {
			t.Fatal("local timeout did not close stream")
		}
		peer.Close()
	}
	if !late {
		client.Close()
	}
	close(release)
	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("pending dial cleanup stuck")
	}
	for i := 0; i < sshd.MaxSessions; i++ {
		select {
		case err := <-lateClosed:
			if err == nil {
				t.Fatal("late channel not closed")
			}
		case <-time.After(time.Second):
			t.Fatal("late channel leaked")
		}
	}
	client.Close()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server fixture stuck")
	}
}
