package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func dynamicRequest(t *testing.T, c net.Conn, host string, port int, kind byte, command byte) byte {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	// Every byte is deliberately a separate write to exercise fragmented frames.
	for _, b := range []byte{5, 2, 2, 0} {
		if _, err := c.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	var method [2]byte
	if _, err := io.ReadFull(c, method[:]); err != nil || method != [2]byte{5, 0} {
		t.Fatalf("method: %v %v", method, err)
	}
	request := []byte{5, command, 0, kind}
	switch kind {
	case 1:
		request = append(request, net.ParseIP(host).To4()...)
	case 4:
		request = append(request, net.ParseIP(host).To16()...)
	case 3:
		request = append(request, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))
	for _, b := range request {
		if _, err := c.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	var reply [10]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 5 || reply[2] != 0 {
		t.Fatalf("reply %v", reply)
	}
	return reply[1]
}

func TestDynamicForwardAddressFrames(t *testing.T) {
	for _, tc := range []struct {
		host string
		kind byte
	}{{"127.0.0.1", 1}, {"::1", 4}, {"remote.invalid", 3}} {
		t.Run(tc.host, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			reached := make(chan string, 1)
			go func() {
				defer server.Close()
				done <- serveDynamicForward(ctx, server, func(_ context.Context, _, address string) (net.Conn, error) {
					reached <- address
					return nil, errors.New("refused")
				})
			}()
			if code := dynamicRequest(t, client, tc.host, 1234, tc.kind, 1); code != 5 {
				t.Fatalf("code %d", code)
			}
			if got := <-reached; got != net.JoinHostPort(tc.host, "1234") {
				t.Fatalf("destination %s", got)
			}
			if err := <-done; err == nil {
				t.Fatal("missing refusal")
			}
		})
	}
}

func TestDynamicForwardProtocolErrors(t *testing.T) {
	for _, command := range []byte{2, 3} {
		server, client := net.Pipe()
		done := make(chan error, 1)
		go func() {
			defer server.Close()
			done <- serveDynamicForward(context.Background(), server, func(context.Context, string, string) (net.Conn, error) {
				t.Error("unsupported command dialed")
				return nil, errors.New("unexpected")
			})
		}()
		// Only the header is sent: unsupported commands must be rejected immediately.
		_ = client.SetDeadline(time.Now().Add(time.Second))
		_, _ = client.Write([]byte{5, 1, 0})
		var method [2]byte
		_, _ = io.ReadFull(client, method[:])
		_, _ = client.Write([]byte{5, command, 0, 1})
		var reply [10]byte
		if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] != 7 {
			t.Fatalf("unsupported %v %v", reply, err)
		}
		client.Close()
		<-done
	}
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { defer server.Close(); done <- serveDynamicForward(context.Background(), server, nil) }()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	_, _ = client.Write([]byte{5, 1, 2})
	var response [2]byte
	if _, err := io.ReadFull(client, response[:]); err != nil || response != [2]byte{5, 255} {
		t.Fatalf("no methods %v %v", response, err)
	}
	client.Close()
	<-done
}

func TestDynamicForwardCLIRealSSHAndRequestIsolation(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, e := backend.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	hostCtx, hostCancel := context.WithCancel(context.Background())
	defer hostCancel()
	ticket := forwardTicket(t, hostCtx, []string{backend.Addr().String()}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := forwardEventWriter{make(chan string, 1)}
	done := make(chan error, 1)
	go func() { done <- forward(ctx, []string{ticket, "--dynamic", "--json"}, output, io.Discard) }()
	var address string
	select {
	case address = <-output.ready:
	case err := <-done:
		t.Fatalf("start %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("start timeout")
	}
	c, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if code := dynamicRequest(t, c, "127.0.0.1", 1, 1, 1); code != 2 {
		t.Fatalf("denied %d", code)
	}
	c.Close()
	// A denied target must not terminate the listener or its SSH connection.
	c, err = net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, portText, _ := net.SplitHostPort(backend.Addr().String())
	port, _ := strconv.Atoi(portText)
	if code := dynamicRequest(t, c, "127.0.0.1", port, 1, 1); code != 0 {
		t.Fatalf("connect %d", code)
	}
	payload := []byte{0, 1, 255, 'x'}
	if _, err = c.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err = io.ReadFull(c, received); err != nil || !bytes.Equal(received, payload) {
		t.Fatalf("echo %v %v", received, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel timeout")
	}
}

func TestDynamicForwardValidationAndCancelHandshake(t *testing.T) {
	for _, args := range [][]string{{"@missing", "--dynamic", "--to", "localhost:80"}, {"@missing", "--dynamic", "--reverse"}} {
		if err := forward(context.Background(), args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("validation %v", err)
		}
	}
	server, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { defer server.Close(); done <- serveDynamicForward(ctx, server, nil) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing canceled read")
		}
	case <-time.After(time.Second):
		t.Fatal("handshake leaked")
	}
}

type shortDynamicDeadline struct{ net.Conn }

func (c shortDynamicDeadline) SetDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		deadline = time.Now().Add(30 * time.Millisecond)
	}
	return c.Conn.SetDeadline(deadline)
}

func TestDynamicForwardHandshakeTimeoutAndInvalidAddress(t *testing.T) {
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func(server net.Conn, done chan error) {
		defer server.Close()
		done <- serveDynamicForward(context.Background(), shortDynamicDeadline{server}, nil)
	}(server, done)
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("timeout %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake lacked deadline")
	}
	client.Close()
	for _, header := range [][]byte{{5, 1, 0, 9}, {5, 1, 1, 1}, {4, 1, 0, 1}} {
		server, client = net.Pipe()
		done = make(chan error, 1)
		go func() { defer server.Close(); done <- serveDynamicForward(context.Background(), server, nil) }()
		_ = client.SetDeadline(time.Now().Add(time.Second))
		_, _ = client.Write([]byte{5, 1, 0})
		var method [2]byte
		_, _ = io.ReadFull(client, method[:])
		_, _ = client.Write(header)
		var reply [10]byte
		if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] == 0 {
			t.Fatalf("invalid request %v %v", reply, err)
		}
		client.Close()
		<-done
	}
}
