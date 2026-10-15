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
	return handshakeWithOptions(ctx, conn, address, cfg, connectionOptions(ctx), limits...)
}
func handshakeWithOptions(ctx context.Context, conn net.Conn, address string, cfg *ssh.ClientConfig, options ConnectionOptions, limits ...time.Time) (*ssh.Client, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	deadline := time.Now().Add(options.ConnectTimeout)
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
	monitored := &connectionConn{Conn: conn}
	cc, ch, req, err := ssh.NewClientConn(monitored, address, cfg)
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
	client := ssh.NewClient(&trackedSSHConn{Conn: cc, transport: monitored}, ch, req)
	cancelWatch := context.AfterFunc(ctx, func() { _ = client.Close() })
	go func() { _ = client.Wait(); cancelWatch() }()
	startKeepalive(ctx, client, options, func() { monitored.timedOut.Store(true) })
	return client, nil
}

// Dial verifies the pinned server key and client authentication for each candidate.
func Dial(ctx context.Context, t Ticket, iface string) (*ssh.Client, error) {
	return DialWithOptions(ctx, t, iface, connectionOptions(ctx))
}
func DialWithOptions(ctx context.Context, t Ticket, iface string, options ConnectionOptions) (*ssh.Client, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	ctx = WithConnectionOptions(ctx, options)
	if !t.ExpiresAt.IsZero() {
		bounded, cancel := context.WithDeadline(ctx, t.ExpiresAt)
		client, err := dialWithOptions(bounded, t, iface, options)
		if err != nil {
			cancel()
		} else {
			go func() { client.Wait(); cancel() }()
		}
		return client, err
	}
	return dialWithOptions(ctx, t, iface, options)
}
func dialWithOptions(ctx context.Context, t Ticket, iface string, options ConnectionOptions) (*ssh.Client, error) {
	cfg, err := clientConfig(t)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, addr := range t.Addresses {
		attempt, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
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
		deadline := time.Now().Add(options.ConnectTimeout)
		conn, e := relayWithOptions(ctx, t, iface, options)
		if e == nil {
			return handshake(ctx, conn, t.RelayURL, cfg, deadline)
		}
		failures = append(failures, fmt.Errorf("relay: %w", e))
	}

	return nil, errors.Join(failures...)
}

// Proxy forwards SSH bytes after verifying a direct candidate's complete handshake.
// OpenSSH performs its own pinned handshake on the second direct connection.
func Proxy(ctx context.Context, t Ticket, iface string, in io.Reader, out io.Writer) error {
	if !t.ExpiresAt.IsZero() {
		bounded, cancel := context.WithDeadline(ctx, t.ExpiresAt)
		defer cancel()
		ctx = bounded
	}
	options := connectionOptions(ctx)
	if err := options.Validate(); err != nil {
		return err
	}
	cfg, err := clientConfig(t)
	if err != nil {
		return err
	}
	var conn net.Conn
	var failures []error
	for _, addr := range t.Addresses {
		attempt, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
		probe, e := netutil.Dial(attempt, addr, iface)
		if e == nil {
			probeOptions := options
			probeOptions.KeepaliveInterval = 0
			client, he := handshakeWithOptions(attempt, probe, addr, cfg, probeOptions)
			e = he
			if he == nil {
				client.Close()
				connectCtx, connectCancel := context.WithTimeout(ctx, options.ConnectTimeout)
				conn, e = netutil.Dial(connectCtx, addr, iface)
				connectCancel()
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
		conn, err = relayWithOptions(ctx, t, iface, options)
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

// The setup timer is detached after success; the returned stream retains the operation lifetime.
func relayWithOptions(ctx context.Context, t Ticket, iface string, options ConnectionOptions) (net.Conn, error) {
	setup, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(options.ConnectTimeout, cancel)
	conn, err := relayConn(setup, t, iface)
	stopped := timer.Stop()
	if err != nil {
		if ctx.Err() != nil {
			cancel()
			return nil, ctx.Err()
		}
		timedOut := setup.Err() != nil && ctx.Err() == nil
		cancel()
		if timedOut {
			return nil, context.DeadlineExceeded
		}
		return nil, err
	}
	if !stopped {
		cancel()
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, context.DeadlineExceeded
	}
	return &connectionLifecycleConn{Conn: conn, cancel: cancel}, nil
}

type connectionLifecycleConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *connectionLifecycleConn) Close() error { c.cancel(); return c.Conn.Close() }
