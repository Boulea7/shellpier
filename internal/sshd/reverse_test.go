package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type reverseHarness struct {
	address string
	config  *ssh.ClientConfig
	cancel  context.CancelFunc
	limits  *ReverseForwardLimits
}

func reverseTestHost(t *testing.T, limits *ReverseForwardLimits) *reverseHarness {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &ssh.ServerConfig{NoClientAuth: true}
	server.AddHostKey(signer)
	h := &reverseHarness{address: listener.Addr().String(), config: &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()), Timeout: 3 * time.Second}, cancel: cancel, limits: limits}
	var wg sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			tcp, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				sc, channels, requests, err := ssh.NewServerConn(tcp, server)
				if err != nil {
					tcp.Close()
					return
				}
				defer sc.Close()
				f := newReverseForwarder(ctx, sc, make(chan struct{}, MaxSessions), limits)
				defer f.Close()
				go func() {
					for request := range requests {
						if !f.HandleRequest(request) {
							_ = request.Reply(false, nil)
						}
					}
				}()
				for channel := range channels {
					_ = channel.Reject(ssh.UnknownChannelType, "test")
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); <-acceptDone; wg.Wait() })
	return h
}
func (h *reverseHarness) client(t *testing.T) *ssh.Client {
	t.Helper()
	c, err := ssh.Dial("tcp", h.address, h.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func reverseRoundTrip(t *testing.T, listener net.Listener) {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer c.Close()
		_, err = io.Copy(c, c)
		accepted <- err
	}()
	tcp, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	tcp.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = tcp.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	tcp.(*net.TCPConn).CloseWrite()
	b, err := io.ReadAll(tcp)
	if err != nil || string(b) != "hello" {
		t.Fatalf("roundtrip %q %v", b, err)
	}
	if err = <-accepted; err != nil {
		t.Fatal(err)
	}
}
func TestReverseGoListenAndCancel(t *testing.T) {
	h := reverseTestHost(t, NewReverseForwardLimits())
	c := h.client(t)
	l, err := c.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reverseRoundTrip(t, l)
	address := l.Addr().String()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if tcp, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		tcp.Close()
		t.Fatal("canceled listener remains")
	}
	// Other global requests still receive a prompt response.
	if ok, _, err := c.SendRequest("probe", true, nil); err != nil || ok {
		t.Fatalf("probe %v %v", ok, err)
	}
}
func TestReverseRejectAndHostLimit(t *testing.T) {
	limits := NewReverseForwardLimits()
	h := reverseTestHost(t, limits)
	a, b := h.client(t), h.client(t)
	for _, host := range []string{"0.0.0.0", "", "*", "192.0.2.1", "127.1", "localhost."} {
		if ok, _, err := a.SendRequest("tcpip-forward", true, ssh.Marshal(reverseListenRequest{Host: host})); err != nil || ok {
			t.Fatalf("accepted %q: %v %v", host, ok, err)
		}
	}
	if ok, _, err := a.SendRequest("tcpip-forward", true, []byte{1}); err != nil || ok {
		t.Fatal("malformed accepted")
	}
	var listeners []net.Listener
	for i := 0; i < MaxSessions; i++ {
		c := a
		if i%2 != 0 {
			c = b
		}
		l, err := c.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
	}
	if l, err := b.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
		t.Fatal("host listener limit bypassed")
	}
	for _, l := range listeners {
		l.Close()
	}
	deadline := time.Now().Add(time.Second)
	for {
		l, err := b.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			l.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener slots not released")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestReverseContextClosesStream(t *testing.T) {
	h := reverseTestHost(t, NewReverseForwardLimits())
	c := h.client(t)
	l, err := c.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() { p, _ := l.Accept(); accepted <- p }()
	local, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	remote := <-accepted
	if remote == nil {
		t.Fatal("no forwarded stream")
	}
	defer remote.Close()
	h.cancel()
	local.SetReadDeadline(time.Now().Add(3 * time.Second))
	var b [1]byte
	if _, err := local.Read(b[:]); err == nil {
		t.Fatal("stream survives cancellation")
	}
	if _, err = remote.Read(b[:]); err == nil {
		t.Fatal("SSH stream survives cancellation")
	}
}
func TestReverseNativeOpenSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix OpenSSH fixture")
	}
	binary, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH unavailable")
	}
	h := reverseTestHost(t, NewReverseForwardLimits())
	// The fixture uses no-client-auth on an isolated loopback endpoint, while the
	// client still pins its generated server key.
	dir := t.TempDir()
	known := filepath.Join(dir, "known_hosts")
	_, port, _ := net.SplitHostPort(h.address)
	// Extract the fixed host key through a pinned handshake callback wrapper.
	cfg := *h.config
	var hostkey ssh.PublicKey
	pin := cfg.HostKeyCallback
	cfg.HostKeyCallback = func(host string, addr net.Addr, key ssh.PublicKey) error { hostkey = key; return pin(host, addr, key) }
	client, err := ssh.Dial("tcp", h.address, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	if err = os.WriteFile(known, append([]byte("[127.0.0.1]:"+port+" "), ssh.MarshalAuthorizedKey(hostkey)...), 0600); err != nil {
		t.Fatal(err)
	}
	// An unused private key avoids consulting an agent or global identity files.
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	os.WriteFile(identity, pem.EncodeToMemory(block), 0600)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	remotePort := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args := []string{"-F", "/dev/null", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "GlobalKnownHostsFile=/dev/null", "-o", "IdentityAgent=none", "-i", identity, "-o", "ExitOnForwardFailure=yes", "-N", "-p", port, "-R", strconv.Itoa(remotePort) + ":" + target.Addr().String(), "test@127.0.0.1"}
	command := exec.CommandContext(ctx, binary, args...)
	logfile, err := os.Create(filepath.Join(dir, "ssh.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logfile.Close()
	command.Stderr = logfile
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); command.Wait() }()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort))
	deadline := time.Now().Add(5 * time.Second)
	var tcp net.Conn
	for {
		tcp, err = net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logfile.Name())
			t.Fatalf("native reverse failed: %v: %s", err, strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer tcp.Close()
	tcp.SetDeadline(time.Now().Add(3 * time.Second))
	tcp.Write([]byte("native"))
	tcp.(*net.TCPConn).CloseWrite()
	b, err := io.ReadAll(tcp)
	if err != nil || string(b) != "native" {
		t.Fatalf("native data %q %v", b, err)
	}
}

func TestReverseHostStreamLimitAndRelease(t *testing.T) {
	limits := NewReverseForwardLimits()
	h := reverseTestHost(t, limits)
	clients := []*ssh.Client{h.client(t), h.client(t)}
	var listeners []net.Listener
	for _, client := range clients {
		listener, err := client.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		listeners = append(listeners, listener)
	}
	var locals, remotes []net.Conn
	defer func() {
		for _, c := range append(locals, remotes...) {
			c.Close()
		}
	}()
	for i := 0; i < MaxSessions; i++ {
		listener := listeners[i%2]
		accepted := make(chan net.Conn, 1)
		go func() { c, _ := listener.Accept(); accepted <- c }()
		local, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		locals = append(locals, local)
		select {
		case remote := <-accepted:
			if remote == nil {
				t.Fatal("stream missing")
			}
			remotes = append(remotes, remote)
		case <-time.After(3 * time.Second):
			t.Fatal("forward accept timeout")
		}
	}
	ninth, err := net.DialTimeout("tcp", listeners[0].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer ninth.Close()
	ninth.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = ninth.Read(b[:]); err == nil {
		t.Fatal("ninth host stream accepted")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("ninth stream not promptly rejected")
	}
	locals[0].Close()
	remotes[0].Close()
	deadline := time.Now().Add(time.Second)
	for len(limits.streams) == MaxSessions {
		if time.Now().After(deadline) {
			t.Fatal("stream slot not released")
		}
		time.Sleep(time.Millisecond)
	}
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := listeners[0].Accept(); accepted <- c }()
	local, err := net.DialTimeout("tcp", listeners[0].Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	select {
	case remote := <-accepted:
		if remote == nil {
			t.Fatal("released stream missing")
		}
		remote.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("slot not reusable")
	}
}

func TestReverseServerAccessAndTTL(t *testing.T) {
	for _, mode := range []AccessMode{AccessFull, AccessSFTP, AccessSFTPReadOnly} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			host, key := signer(t), signer(t)
			endpoint, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer endpoint.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := endpoint.Accept()
				if err != nil {
					done <- err
					return
				}
				done <- (&Server{Signer: host, AuthorizedKey: key.PublicKey(), AccessMode: mode, Reverse: NewReverseForwardLimits()}).ServeConn(ctx, conn)
			}()
			c, err := ssh.Dial("tcp", endpoint.Addr().String(), &ssh.ClientConfig{User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(key)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			listener, err := c.Listen("tcp", "127.0.0.1:0")
			if mode != AccessFull {
				if err == nil {
					listener.Close()
					t.Fatal("restricted mode permits reverse forwarding")
				}
				c.Close()
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			reverseRoundTrip(t, listener)
			address := listener.Addr().String()
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("TTL did not finish server")
			}
			tcp, err := net.DialTimeout("tcp", address, time.Second)
			if err == nil {
				tcp.Close()
				t.Fatal("TTL leaves reverse listener")
			}
		})
	}
}
