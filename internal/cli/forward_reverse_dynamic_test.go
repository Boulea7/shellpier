package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func startReverseDynamic(t *testing.T, ctx context.Context, ticket string) (reverseEvent, <-chan error) {
	t.Helper()
	writer := reverseEventWriter{make(chan reverseEvent, 1)}
	done := make(chan error, 1)
	go func() {
		done <- forward(ctx, []string{ticket, "--reverse", "--dynamic", "--listen", "0", "--json"}, writer, io.Discard)
	}()
	select {
	case event := <-writer.ready:
		if event.Direction != "reverse-dynamic" || event.RemoteAddress == "" || event.Target != "" || event.LocalAddress != "" || event.LocalTarget != "" {
			t.Fatalf("unexpected readiness: %+v", event)
		}
		return event, done
	case err := <-done:
		t.Fatalf("start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("readiness timeout")
	}
	return reverseEvent{}, done
}

func TestReverseDynamicForwardRealSSH(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event, done := startReverseDynamic(t, ctx, reverseTicket(t, context.Background(), time.Minute, ""))
	// Refused destinations and unsupported commands must leave the listener usable.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	for _, command := range []byte{1, 2, 3} {
		conn, err := net.Dial("tcp", event.RemoteAddress)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if command == 1 {
			if code := dynamicRequest(t, conn, "127.0.0.1", refusedPort, 1, command); code != 5 {
				t.Fatalf("refusal code %d", code)
			}
		} else {
			_, _ = conn.Write([]byte{5, 1, 0})
			var method [2]byte
			if _, err := io.ReadFull(conn, method[:]); err != nil {
				t.Fatal(err)
			}
			_, _ = conn.Write([]byte{5, command, 0, 1})
			var reply [10]byte
			if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[1] != 7 {
				t.Fatalf("unsupported %v %v", reply, err)
			}
		}
		conn.Close()
	}
	conn, err := net.Dial("tcp", event.RemoteAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, portText, _ := net.SplitHostPort(backend.Addr().String())
	port, _ := strconv.Atoi(portText)
	// Domain-form localhost requires the control-side DialContext to resolve DNS.
	if code := dynamicRequest(t, conn, "localhost", port, 3, 1); code != 0 {
		t.Fatalf("CONNECT %d", code)
	}
	payload := []byte{0, 255, 1, 'x', 0, 128}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload %v %v", got, err)
	}
	cancel()
	assertReverseStopped(t, done, event.RemoteAddress)
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("active stream survived cancellation")
	}
}

func TestReverseDynamicForwardExpiryTransportAndHandshake(t *testing.T) {
	for _, mode := range []string{"expiry", "transport", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			host, stopHost := context.WithCancel(context.Background())
			defer stopHost()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lifetime := time.Minute
			if mode == "expiry" {
				lifetime = time.Second
			}
			event, done := startReverseDynamic(t, ctx, reverseTicket(t, host, lifetime, ""))
			conn, err := net.Dial("tcp", event.RemoteAddress)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Leave an incomplete SOCKS handshake pending during shutdown.
			_, _ = conn.Write([]byte{5})
			if mode == "transport" {
				stopHost()
			}
			if mode == "cancel" {
				cancel()
			}
			assertReverseStopped(t, done, event.RemoteAddress)
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := conn.Read(make([]byte, 1)); err == nil {
				t.Fatal("pending handshake survived")
			}
		})
	}
}

type noDynamicDeadline struct{ net.Conn }

func (c noDynamicDeadline) SetDeadline(time.Time) error {
	panic("SSH channels do not support deadlines")
}
func TestDynamicForwardHandshakeWithoutSocketDeadline(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		done <- serveDynamicForwardHandshake(context.Background(), noDynamicDeadline{server}, nil, 30*time.Millisecond)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake did not time out")
	}
}
