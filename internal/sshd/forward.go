package sshd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const DefaultForwardDialTimeout = 5 * time.Second

// ForwardPolicy permits only explicitly enrolled host:port destinations.
// Construct it once before serving connections; its allowlist is immutable.
type ForwardPolicy struct {
	allowed map[string]struct{}
	slots   chan struct{}
}

func NewForwardPolicy(targets []string) (*ForwardPolicy, error) {
	p := &ForwardPolicy{allowed: make(map[string]struct{}, len(targets)), slots: make(chan struct{}, MaxSessions)}
	for _, target := range targets {
		normalized, err := NormalizeForwardTarget(target)
		if err != nil {
			return nil, err
		}
		p.allowed[normalized] = struct{}{}
	}
	return p, nil
}

// NormalizeForwardTarget canonicalizes literal IPs and ASCII DNS names, not DNS answers.
// Grants are exact names and ports; wildcards, zones, service names and IP shorthand fail.
func NormalizeForwardTarget(target string) (string, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil || host == "" || len(host) > 253 {
		return "", errors.New("forward target requires a host and numeric port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return "", errors.New("forward target port must be canonical decimal 1..65535")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return "", errors.New("forward target IP zones are not supported")
		}
		host = ip.Unmap().String()
	} else {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if strings.HasPrefix(host, "0x") && strings.Trim(host[2:], "0123456789abcdef") == "" {
			return "", errors.New("ambiguous forward IP address")
		}
		if host == "" || strings.Trim(host, "0123456789.") == "" {
			return "", errors.New("ambiguous forward IP address")
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", errors.New("invalid forward DNS name")
			}
			for _, c := range label {
				if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
					return "", errors.New("forward DNS names must use ASCII letters, digits and hyphens")
				}
			}
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

type forwardChannelData struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

// Serve handles one direct-tcpip channel. The caller owns shared MaxSessions
// admission and waits for this method before releasing its slot.
func (p *ForwardPolicy) Serve(ctx context.Context, incoming ssh.NewChannel) error {
	reject := func(reason ssh.RejectionReason, message string) error {
		_ = incoming.Reject(reason, message)
		return errors.New(message)
	}
	if incoming.ChannelType() != "direct-tcpip" {
		return reject(ssh.UnknownChannelType, "unsupported forwarding channel")
	}
	if p == nil || len(p.allowed) == 0 {
		return reject(ssh.Prohibited, "forwarding is disabled")
	}
	var request forwardChannelData
	if len(incoming.ExtraData()) > 4096 || ssh.Unmarshal(incoming.ExtraData(), &request) != nil || request.Port > 65535 || len(request.OriginHost) > 253 || request.OriginPort > 65535 {
		return reject(ssh.Prohibited, "invalid forwarding request")
	}
	target, err := NormalizeForwardTarget(net.JoinHostPort(request.Host, strconv.FormatUint(uint64(request.Port), 10)))
	if err != nil {
		return reject(ssh.Prohibited, "invalid forwarding target")
	}
	if _, ok := p.allowed[target]; !ok {
		return reject(ssh.Prohibited, "forwarding target is not allowed")
	}
	select {
	case p.slots <- struct{}{}:
	default:
		return reject(ssh.ResourceShortage, "host forwarding limit reached")
	}
	defer func() { <-p.slots }()
	dialCtx, cancel := context.WithTimeout(ctx, DefaultForwardDialTimeout)
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", target)
	cancel()
	if err != nil {
		return reject(ssh.ConnectionFailed, "forwarding target is unavailable")
	}
	defer conn.Close()
	channel, requests, err := incoming.Accept()
	if err != nil {
		return err
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)
	return BridgeForward(ctx, channel, conn)
}

// BridgeForward preserves TCP/SSH EOF half-closes, while cancellation or a copy
// error closes both peers. It never reconnects or replays bytes.
func BridgeForward(ctx context.Context, left, right io.ReadWriteCloser) error {
	closeBoth := func() { _ = left.Close(); _ = right.Close() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	results := make(chan error, 2)
	copyHalf := func(destination io.ReadWriteCloser, source io.Reader) {
		_, err := io.Copy(destination, source)
		if closer, ok := destination.(interface{ CloseWrite() error }); ok {
			if e := closer.CloseWrite(); err == nil {
				err = e
			}
		} else {
			_ = destination.Close()
		}
		if err != nil {
			closeBoth()
		}
		results <- err
	}
	go copyHalf(left, right)
	go copyHalf(right, left)
	first, second := <-results, <-results
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(first, second)
}
