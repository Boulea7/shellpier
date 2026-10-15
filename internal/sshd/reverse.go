package sshd

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ReverseForwardLimits bounds listeners and streams across a host's SSH connections.
// Its zero value is ready to use; keep one instance per Server.
type ReverseForwardLimits struct {
	once      sync.Once
	listeners chan struct{}
	streams   chan struct{}
}

// NewReverseForwardLimits creates limits shared by all connections of one host.
func NewReverseForwardLimits() *ReverseForwardLimits { return &ReverseForwardLimits{} }

func (l *ReverseForwardLimits) init() {
	l.once.Do(func() { l.listeners = make(chan struct{}, MaxSessions); l.streams = make(chan struct{}, MaxSessions) })
}

type reverseListenRequest struct {
	Host string
	Port uint32
}
type reverseForwarder struct {
	ctx       context.Context
	cancel    context.CancelFunc
	conn      *ssh.ServerConn
	sessions  chan struct{}
	limits    *ReverseForwardLimits
	mu        sync.Mutex
	closed    bool
	listeners map[reverseListenRequest]net.Listener
	streams   map[net.Conn]struct{}
	wg        sync.WaitGroup
}

func newReverseForwarder(ctx context.Context, conn *ssh.ServerConn, sessions chan struct{}, limits *ReverseForwardLimits) *reverseForwarder {
	if limits == nil {
		limits = NewReverseForwardLimits()
	}
	limits.init()
	ctx, cancel := context.WithCancel(ctx)
	f := &reverseForwarder{ctx: ctx, cancel: cancel, conn: conn, sessions: sessions, limits: limits, listeners: make(map[reverseListenRequest]net.Listener), streams: make(map[net.Conn]struct{})}
	go func() { <-ctx.Done(); f.Close() }()
	return f
}

// HandleRequest consumes only standard TCP reverse forwarding requests. A nil
// receiver rejects forwarding, allowing restricted sessions to use the same hook.
func (f *reverseForwarder) HandleRequest(req *ssh.Request) bool {
	if req.Type != "tcpip-forward" && req.Type != "cancel-tcpip-forward" {
		return false
	}
	var requested reverseListenRequest
	if f == nil || len(req.Payload) > 1024 || ssh.Unmarshal(req.Payload, &requested) != nil || requested.Port > 65535 {
		_ = req.Reply(false, nil)
		return true
	}
	host := requested.Host
	if host == "localhost" {
		host = "127.0.0.1"
	} else {
		ip, err := netip.ParseAddr(host)
		if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
			_ = req.Reply(false, nil)
			return true
		}
		host = ip.Unmap().String()
	}
	if req.Type == "cancel-tcpip-forward" {
		f.mu.Lock()
		listener, ok := f.listeners[requested]
		if ok {
			delete(f.listeners, requested)
		}
		f.mu.Unlock()
		if ok {
			_ = listener.Close()
		}
		_ = req.Reply(ok, nil)
		return true
	}
	// A port-zero listener is not useful without the assigned-port response.
	if requested.Port == 0 && !req.WantReply {
		return true
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		_ = req.Reply(false, nil)
		return true
	}
	if requested.Port != 0 {
		if _, ok := f.listeners[requested]; ok {
			f.mu.Unlock()
			_ = req.Reply(false, nil)
			return true
		}
	}
	select {
	case f.limits.listeners <- struct{}{}:
	default:
		f.mu.Unlock()
		_ = req.Reply(false, nil)
		return true
	}
	network := "tcp4"
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
		network = "tcp6"
	}
	listener, err := net.Listen(network, net.JoinHostPort(host, strconv.Itoa(int(requested.Port))))
	if err != nil {
		<-f.limits.listeners
		f.mu.Unlock()
		_ = req.Reply(false, nil)
		return true
	}
	actual := requested
	actual.Port = uint32(listener.Addr().(*net.TCPAddr).Port)
	f.listeners[actual] = listener
	f.wg.Add(1)
	f.mu.Unlock()
	var reply []byte
	if requested.Port == 0 {
		reply = ssh.Marshal(struct{ Port uint32 }{actual.Port})
	}
	if err := req.Reply(true, reply); err != nil {
		_ = listener.Close()
	}
	go f.accept(actual, listener)
	return true
}

func (f *reverseForwarder) accept(key reverseListenRequest, listener net.Listener) {
	defer f.wg.Done()
	defer func() {
		_ = listener.Close()
		<-f.limits.listeners
		f.mu.Lock()
		if f.listeners[key] == listener {
			delete(f.listeners, key)
		}
		f.mu.Unlock()
	}()
	for {
		tcp, err := listener.Accept()
		if err != nil {
			return
		}
		select {
		case f.limits.streams <- struct{}{}:
		default:
			_ = tcp.Close()
			continue
		}
		select {
		case f.sessions <- struct{}{}:
		default:
			<-f.limits.streams
			_ = tcp.Close()
			continue
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			<-f.sessions
			<-f.limits.streams
			_ = tcp.Close()
			return
		}
		f.streams[tcp] = struct{}{}
		f.wg.Add(1)
		f.mu.Unlock()
		go f.stream(key, tcp)
	}
}

func (f *reverseForwarder) stream(key reverseListenRequest, tcp net.Conn) {
	defer f.wg.Done()
	defer func() {
		_ = tcp.Close()
		<-f.sessions
		<-f.limits.streams
		f.mu.Lock()
		delete(f.streams, tcp)
		f.mu.Unlock()
	}()
	origin := tcp.RemoteAddr().(*net.TCPAddr)
	payload := ssh.Marshal(forwardChannelData{Host: key.Host, Port: key.Port, OriginHost: origin.IP.String(), OriginPort: uint32(origin.Port)})
	type opened struct {
		channel  ssh.Channel
		requests <-chan *ssh.Request
		err      error
	}
	done := make(chan opened, 1)
	go func() { ch, reqs, err := f.conn.OpenChannel("forwarded-tcpip", payload); done <- opened{ch, reqs, err} }()
	timer := time.NewTimer(DefaultForwardDialTimeout)
	defer timer.Stop()
	var result opened
	select {
	case result = <-done:
	case <-timer.C:
		// SSH channel-open has no context API. Closing this transport bounds both the
		// blocked open and all its streams, without reconnecting or replaying data.
		_ = f.conn.Close()
		result = <-done
	case <-f.ctx.Done():
		_ = f.conn.Close()
		result = <-done
	}
	if result.err != nil {
		return
	}
	defer result.channel.Close()
	go ssh.DiscardRequests(result.requests)
	_ = BridgeForward(f.ctx, result.channel, tcp)
}

// Close withdraws this connection's listeners and closes its streams.
func (f *reverseForwarder) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.cancel()
	for _, listener := range f.listeners {
		_ = listener.Close()
	}
	for tcp := range f.streams {
		_ = tcp.Close()
	}
	f.mu.Unlock()
	// Also unblock any SSH channel-open waiting for an unresponsive client.
	_ = f.conn.Close()
	f.wg.Wait()
}
