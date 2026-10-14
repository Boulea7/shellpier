package access

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"time"

	"github.com/Boulea7/shellpier/internal/relay"
	"golang.org/x/crypto/ssh"
)

type RetryOptions struct {
	Retries  int
	MinDelay time.Duration
	MaxDelay time.Duration
}

func DefaultRetryOptions() RetryOptions {
	return RetryOptions{Retries: 2, MinDelay: 200 * time.Millisecond, MaxDelay: 3 * time.Second}
}
func (o RetryOptions) Validate() error {
	if o.Retries < 0 || o.Retries > 10 || o.MinDelay <= 0 || o.MaxDelay < o.MinDelay || o.MaxDelay > time.Minute {
		return errors.New("retry options require retries 0..10, 0 < retry-min <= retry-max <= 1m")
	}
	return nil
}

// DialRetry retries only connection establishment, never remote operations.
func DialRetry(ctx context.Context, t Ticket, iface string, options RetryOptions) (*ssh.Client, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if _, err := clientConfig(t); err != nil {
		return nil, err
	}
	if iface != "" && t.RelayURL != "" && len(t.Addresses) == 0 {
		return nil, errors.New("interface binding is supported only for direct access")
	}
	var cancel context.CancelFunc
	if !t.ExpiresAt.IsZero() {
		ctx, cancel = context.WithDeadline(ctx, t.ExpiresAt)
	}
	client, err := retryDial(ctx, options, func() (*ssh.Client, error) { return Dial(ctx, t, iface) })
	if cancel != nil {
		if err != nil {
			cancel()
		} else {
			go func() { client.Wait(); cancel() }()
		}
	}
	return client, err
}
func retryDial(ctx context.Context, options RetryOptions, dial func() (*ssh.Client, error)) (*ssh.Client, error) {
	delay := options.MinDelay
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		client, err := dial()
		if err == nil {
			return client, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt >= options.Retries || !retryableConnection(err) {
			return nil, err
		}
		// Equal jitter stays within the current bounded exponential delay.
		wait := delay/2 + time.Duration(rand.Int64N(int64(delay-delay/2)+1))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay >= options.MaxDelay/2 {
			delay = options.MaxDelay
		} else {
			delay *= 2
		}
	}
}
func retryableConnection(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, relay.ErrSessionClosed) || errors.Is(err, relay.ErrUnauthorized) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, relay.ErrInvalidURL) || errors.Is(err, relay.ErrRedirectDenied) || errors.Is(err, relay.ErrHandshakeRejected) {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !retryableConnection(child) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return retryableConnection(wrapped)
	}
	if errors.Is(err, relay.ErrUnavailable) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return !dns.IsNotFound && (dns.IsTimeout || dns.IsTemporary)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || retryablePlatformConnection(err) {
		return true
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return true
	}
	return false
}
