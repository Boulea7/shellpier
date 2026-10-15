package sshd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func forwardSSHClient(t *testing.T, parent context.Context, policy *ForwardPolicy) (*ssh.Client, <-chan struct{}) {
	t.Helper()
	key, host := signer(t), signer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		ctx, cancel := context.WithCancel(parent)
		defer cancel()
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		defer stop()
		config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, provided ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.PublicKey().Marshal(), provided.Marshal()) {
				return nil, errors.New("denied")
			}
			return nil, nil
		}}
		config.AddHostKey(host)
		server, channels, requests, err := ssh.NewServerConn(raw, config)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		var workers sync.WaitGroup
		for incoming := range channels {
			workers.Add(1)
			go func() { defer workers.Done(); _ = policy.Serve(ctx, incoming) }()
		}
		cancel()
		workers.Wait()
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "shellpier", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("forward server did not stop")
		}
	})
	return client, done
}

func TestForwardTargetNormalization(t *testing.T) {
	for input, want := range map[string]string{"EXAMPLE.com.:80": "example.com:80", "[::ffff:127.0.0.1]:80": "127.0.0.1:80", "[0:0:0:0:0:0:0:1]:80": "[::1]:80", "localhost:443": "localhost:443"} {
		got, err := NormalizeForwardTarget(input)
		if err != nil || got != want {
			t.Errorf("%s: %s %v", input, got, err)
		}
	}
	for _, input := range []string{"*:80", ":80", "127.1:80", "127.0.0.01:80", "2130706433:80", "0x7f000001:80", "127.0.0.1:0", "localhost:080", "localhost:+80", "[fe80::1%en0]:80", "a..b:80", "a_b:80", "https://host:80", "-bad:80", "[::1]:65536", " host:80"} {
		if _, err := NormalizeForwardTarget(input); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}
func TestForwardAllowlistHTTPAndDenial(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "forwarded") }))
	defer backend.Close()
	target := strings.TrimPrefix(backend.URL, "http://")
	policy, err := NewForwardPolicy([]string{target})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := forwardSSHClient(t, context.Background(), policy)
	conn, err := client.Dial("tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(conn, "GET / HTTP/1.0\r\nHost: local\r\n\r\n")
	response, err := io.ReadAll(conn)
	if err != nil || !bytes.Contains(response, []byte("forwarded")) {
		t.Fatalf("HTTP forwarding: %q %v", response, err)
	}
	host, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	forged := ssh.Marshal(forwardChannelData{Host: "localhost", Port: uint32(port), OriginHost: host, OriginPort: uint32(port)})
	if channel, requests, err := client.OpenChannel("direct-tcpip", forged); err == nil {
		channel.Close()
		go ssh.DiscardRequests(requests)
		t.Fatal("origin address authorized a different destination")
	}
	denied, _ := forwardSSHClient(t, context.Background(), nil)
	if conn, err := denied.Dial("tcp", target); err == nil {
		conn.Close()
		t.Fatal("disabled policy allowed forwarding")
	}
	if conn, err := client.Dial("tcp", "localhost:"+strings.Split(target, ":")[1]); err == nil {
		conn.Close()
		t.Fatal("DNS alias bypassed exact target")
	}
}
func TestForwardHalfCloseAndCancellation(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	received := make(chan net.Conn, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		received <- conn
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		_, _ = conn.Write(append([]byte("reply:"), data...))
	}()
	policy, _ := NewForwardPolicy([]string{backend.Addr().String()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, done := forwardSSHClient(t, ctx, policy)
	conn, err := client.Dial("tcp", backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("payload"))
	if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != "reply:payload" {
		t.Fatalf("half-close lost response: %q %v", data, err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("backend not reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close SSH")
	}
}
func TestForwardHostWideCapacityAndTTL(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	closed := make(chan struct{}, MaxSessions)
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(io.Discard, conn); closed <- struct{}{} }()
		}
	}()
	policy, _ := NewForwardPolicy([]string{backend.Addr().String()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, _ := forwardSSHClient(t, ctx, policy)
	second, _ := forwardSSHClient(t, ctx, policy)
	var channels []net.Conn
	for i := 0; i < MaxSessions; i++ {
		client := first
		if i%2 != 0 {
			client = second
		}
		conn, err := client.Dial("tcp", backend.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		channels = append(channels, conn)
	}
	defer func() {
		for _, conn := range channels {
			conn.Close()
		}
	}()
	if extra, err := second.Dial("tcp", backend.Addr().String()); err == nil {
		extra.Close()
		t.Fatal("host-wide capacity bypassed across SSH connections")
	} else {
		var rejection *ssh.OpenChannelError
		if !errors.As(err, &rejection) || rejection.Reason != ssh.ResourceShortage {
			t.Fatalf("unexpected rejection: %v", err)
		}
	}
	cancel()
	for i := 0; i < MaxSessions; i++ {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("cancellation left target open")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(policy.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(policy.slots) != 0 {
		t.Fatal("forward slots leaked")
	}
	ttlCtx, ttlCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer ttlCancel()
	ttlClient, done := forwardSSHClient(t, ttlCtx, policy)
	channel, err := ttlClient.Dial("tcp", backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TTL did not close forwarding")
	}
	buffer := make([]byte, 1)
	if _, err := channel.Read(buffer); err == nil {
		t.Fatal("TTL left client channel open")
	}
}
