package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Boulea7/shellpier/internal/netutil"
	"github.com/Boulea7/shellpier/internal/relay"
	"golang.org/x/crypto/ssh"
)

func clientConfig(t Ticket) (*ssh.ClientConfig, error) {
	if err := t.configured(); err != nil {
		return nil, err
	}
	if t.Kind != "access" {
		return nil, errors.New("requires access ticket")
	}
	host, err := publicKey(t.HostKey)
	if err != nil {
		return nil, err
	}
	signer, err := privateKey(t.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{User: "shellpier", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(host), HostKeyAlgorithms: []string{ssh.KeyAlgoED25519}, Timeout: 10 * time.Second}, nil
}
func relayConn(ctx context.Context, t Ticket, iface string) (net.Conn, error) {
	if iface != "" {
		return nil, errors.New("interface binding is supported only for direct access")
	}
	return relay.DialClient(ctx, t.RelayURL, relay.Session{ID: t.ID, ClientToken: t.RelayToken, ExpiresAt: t.ExpiresAt})
}
func handshake(ctx context.Context, conn net.Conn, address string, cfg *ssh.ClientConfig, limits ...time.Time) (*ssh.Client, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for _, limit := range limits {
		if limit.Before(deadline) {
			deadline = limit
		}
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	cc, ch, req, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		stop()
		conn.Close()
		return nil, err
	}
	if !stop() || ctx.Err() != nil {
		cc.Close()
		return nil, ctx.Err()
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		cc.Close()
		return nil, err
	}
	client := ssh.NewClient(cc, ch, req)
	cancelWatch := context.AfterFunc(ctx, func() { _ = client.Close() })
	go func() { _ = client.Wait(); cancelWatch() }()
	return client, nil
}

// Dial verifies the pinned server key and client authentication for each candidate.
func Dial(ctx context.Context, t Ticket, iface string) (*ssh.Client, error) {
	cfg, err := clientConfig(t)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, addr := range t.Addresses {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := netutil.Dial(attempt, addr, iface)
		if err == nil {
			limit, _ := attempt.Deadline()
			client, e := handshake(ctx, conn, addr, cfg, limit)
			err = e
			if e == nil {
				cancel()
				return client, nil
			}
		}
		cancel()
		failures = append(failures, fmt.Errorf("direct %s: %w", addr, err))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if t.RelayURL != "" {
		conn, e := relayConn(ctx, t, iface)
		if e == nil {
			return handshake(ctx, conn, t.RelayURL, cfg)
		}
		failures = append(failures, fmt.Errorf("relay: %w", e))
	}
	return nil, errors.Join(failures...)
}

// Proxy forwards SSH bytes after verifying a direct candidate's complete handshake.
// OpenSSH performs its own pinned handshake on the second direct connection.
func Proxy(ctx context.Context, t Ticket, iface string, in io.Reader, out io.Writer) error {
	cfg, err := clientConfig(t)
	if err != nil {
		return err
	}
	var conn net.Conn
	var failures []error
	for _, addr := range t.Addresses {
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		probe, e := netutil.Dial(attempt, addr, iface)
		if e == nil {
			client, he := handshake(attempt, probe, addr, cfg)
			e = he
			if he == nil {
				client.Close()
				conn, e = netutil.Dial(ctx, addr, iface)
			}
		}
		cancel()
		if e == nil {
			break
		}
		failures = append(failures, e)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if conn == nil && t.RelayURL != "" {
		conn, err = relayConn(ctx, t, iface)
		if err != nil {
			failures = append(failures, err)
		}
	}
	if conn == nil {
		return errors.Join(failures...)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	upload := make(chan error, 1)
	go func() {
		_, e := io.Copy(conn, in)
		if half, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = conn.Close()
		}
		upload <- e
	}()
	_, err = io.Copy(out, conn)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case e := <-upload:
		if err == nil {
			err = e
		}
	default:
	}
	return err
}
