package access

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrKeepaliveTimeout means the connection stopped responding; remote effects remain unknown.
var ErrKeepaliveTimeout = errors.New("SSH keepalive response timed out")

// ConnectionOptions separates connection setup from operation and authorization lifetimes.
// KeepaliveInterval zero disables SSH probes. Replies with either success value count as alive.
type ConnectionOptions struct {
	ConnectTimeout    time.Duration
	KeepaliveInterval time.Duration
	KeepaliveTimeout  time.Duration
}

func DefaultConnectionOptions() ConnectionOptions {
	return ConnectionOptions{10 * time.Second, 15 * time.Second, 45 * time.Second}
}
func (o ConnectionOptions) Validate() error {
	if o.ConnectTimeout <= 0 || o.ConnectTimeout > 5*time.Minute || o.KeepaliveInterval < 0 || o.KeepaliveInterval > time.Hour || o.KeepaliveInterval > 0 && (o.KeepaliveTimeout <= 0 || o.KeepaliveTimeout > time.Hour) {
		return errors.New("connection options require 0 < connect-timeout <= 5m, 0 <= keepalive-interval <= 1h, and 0 < enabled keepalive-timeout <= 1h")
	}
	return nil
}

type connectionOptionsKey struct{}

// WithConnectionOptions overrides defaults for existing Dial and DialRetry callers.
func WithConnectionOptions(ctx context.Context, options ConnectionOptions) context.Context {
	return context.WithValue(ctx, connectionOptionsKey{}, options)
}
func connectionOptions(ctx context.Context) ConnectionOptions {
	if value, ok := ctx.Value(connectionOptionsKey{}).(ConnectionOptions); ok {
		return value
	}
	return DefaultConnectionOptions()
}

// startKeepalive has one outstanding SSH request at most. Closing the transport releases it.
func startKeepalive(ctx context.Context, client *ssh.Client, options ConnectionOptions, onTimeout func()) {
	if options.KeepaliveInterval == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(options.KeepaliveInterval)
		defer ticker.Stop()
		done := make(chan struct{})
		go func() { _ = client.Wait(); close(done) }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
			}
			reply := make(chan error, 1)
			go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); reply <- err }()
			timer := time.NewTimer(options.KeepaliveTimeout)
			select {
			case <-ctx.Done():
				timer.Stop()
				_ = client.Close()
				return
			case <-done:
				timer.Stop()
				return
			case err := <-reply:
				timer.Stop()
				if err != nil {
					_ = client.Close()
					return
				}
			case <-timer.C:
				if ctx.Err() != nil {
					_ = client.Close()
					return
				}
				if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
					_ = client.Close()
					return
				}
				onTimeout()
				_ = client.Close()
				return
			}
		}
	}()
}

type connectionConn struct {
	net.Conn
	timedOut atomic.Bool
}

func (c *connectionConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if err != nil && c.timedOut.Load() {
		err = errors.Join(ErrKeepaliveTimeout, err)
	}
	return n, err
}
func (c *connectionConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err != nil && c.timedOut.Load() {
		err = errors.Join(ErrKeepaliveTimeout, err)
	}
	return n, err
}

// trackedSSHConn keeps transport failure attribution available without waiting for shutdown.
type trackedSSHConn struct {
	ssh.Conn
	transport *connectionConn
}

func (c *trackedSSHConn) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	ok, data, err := c.Conn.SendRequest(name, wantReply, payload)
	return ok, data, c.connectionError(err)
}
func (c *trackedSSHConn) OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	channel, requests, err := c.Conn.OpenChannel(name, data)
	return channel, requests, c.connectionError(err)
}
func (c *trackedSSHConn) connectionError(err error) error {
	if err == nil || errors.Is(err, ErrKeepaliveTimeout) || !c.transport.timedOut.Load() {
		return err
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
		return err
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return errors.Join(err, ErrKeepaliveTimeout)
	}
	return err
}

// ConnectionError restores a keepalive cause lost by SSH session or subsystem APIs.
// It never waits, and preserves successful results, known exits, and local errors.
func ConnectionError(client *ssh.Client, err error) error {
	if client != nil {
		if tracked, ok := client.Conn.(*trackedSSHConn); ok {
			return tracked.connectionError(err)
		}
	}
	return err
}
