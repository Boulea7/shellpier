package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/rand/v2"
	"net"
	"time"

	"github.com/Boulea7/shellpier/internal/relay"
)

const defaultSessionTTL = 3 * time.Hour
const defaultReconnectMin = time.Second
const defaultReconnectMax = 30 * time.Second
const stableRelayConnection = 10 * time.Second

type hostBackoff struct {
	min, max, current time.Duration
	random            func(int64) int64
}

func newHostBackoff(min, max time.Duration) *hostBackoff {
	return &hostBackoff{min: min, max: max, current: min, random: rand.Int64N}
}
func (b *hostBackoff) reset() { b.current = b.min }
func (b *hostBackoff) next() time.Duration {
	upper := b.current
	lower := upper / 2
	if lower < b.min {
		lower = b.min
	}
	delay := lower
	if upper > lower {
		delay += time.Duration(b.random(int64(upper-lower) + 1))
	}
	if b.current >= b.max/2 {
		b.current = b.max
	} else {
		b.current *= 2
	}
	return delay
}
func waitHostRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
func permanentRelayError(err error) bool {
	if errors.Is(err, relay.ErrSessionClosed) || errors.Is(err, relay.ErrUnauthorized) || errors.Is(err, relay.ErrInvalidURL) || errors.Is(err, relay.ErrRedirectDenied) || errors.Is(err, relay.ErrHandshakeRejected) {
		return true
	}
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var roots x509.SystemRootsError
	var algorithm x509.InsecureAlgorithmError
	return errors.As(err, &verification) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &roots) || errors.As(err, &algorithm)
}

// A successful handshake alone is insufficient to reset backoff: peers that
// disconnect immediately must not cause a tight reconnect loop.
func reconnectHost(ctx context.Context, min, max time.Duration, dial func(context.Context) (net.Conn, error), serve func(context.Context, net.Conn), notice func()) error {
	backoff := newHostBackoff(min, max)
	for ctx.Err() == nil {
		conn, err := dial(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if permanentRelayError(err) {
				return err
			}
			notice()
		} else {
			started := time.Now()
			serve(ctx, conn)
			_ = conn.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Since(started) >= stableRelayConnection {
				backoff.reset()
				continue
			}
		}
		if err := waitHostRetry(ctx, backoff.next()); err != nil {
			return err
		}
	}
	return ctx.Err()
}
