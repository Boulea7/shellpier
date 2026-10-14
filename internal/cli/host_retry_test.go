package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/relay"
)

func TestDefaultSessionTTLThreeHours(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	ticketPath := filepath.Join(dir, "access.json")
	started := time.Now()
	state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--output", ticketPath, "--json")
	ticket, err := access.Read(ticketPath)
	if err != nil {
		t.Fatal(err)
	}
	assertThreeHourExpiry(t, ticket.ExpiresAt, started)
	invoke(t, "stop", "--state", state)
	awaitHost(t, done, state)
	srv, err := relay.New(strings.Repeat("k", 32), 4)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()
	defer srv.Close()
	keyPath := filepath.Join(dir, "relay.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	invitation := filepath.Join(dir, "invitation")
	started = time.Now()
	if err := invite(context.Background(), []string{"--relay", httpServer.URL, "--token-file", keyPath, "--out", invitation}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	ticket, err = access.Read(filepath.Join(invitation, "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertThreeHourExpiry(t, ticket.ExpiresAt, started)
}
func assertThreeHourExpiry(t *testing.T, expiry, started time.Time) {
	t.Helper()
	duration := expiry.Sub(started)
	if duration < 3*time.Hour-2*time.Second || duration > 3*time.Hour+10*time.Second {
		t.Fatalf("default expiry was %s", duration)
	}
}
func TestHostBackoffBoundsJitterAndReset(t *testing.T) {
	b := newHostBackoff(time.Second, 30*time.Second)
	b.random = func(n int64) int64 { return 0 }
	for i, want := range []time.Duration{time.Second, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second} {
		if got := b.next(); got != want {
			t.Fatalf("step %d = %s, want %s", i, got, want)
		}
	}
	b.random = func(n int64) int64 { return n - 1 }
	if got := b.next(); got != 30*time.Second {
		t.Fatal(got)
	}
	b.reset()
	if got := b.next(); got != time.Second {
		t.Fatalf("reset did not restore minimum: %s", got)
	}
	b = newHostBackoff(time.Millisecond, 13*time.Millisecond)
	seen := map[time.Duration]bool{}
	for range 100 {
		got := b.next()
		if got < time.Millisecond || got > 13*time.Millisecond {
			t.Fatal("backoff outside bounds")
		}
		seen[got] = true
	}
	if len(seen) < 3 {
		t.Fatal("jitter did not vary delay")
	}
}
func TestHostRetryWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitHostRetry(ctx, time.Hour) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if err := waitHostRetry(expired, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestHostRetryPermanentStops(t *testing.T) {
	for _, cause := range []error{relay.ErrUnauthorized, relay.ErrSessionClosed} {
		attempts := 0
		err := reconnectHost(context.Background(), time.Millisecond, time.Second, func(context.Context) (net.Conn, error) {
			attempts++
			return nil, fmt.Errorf("handshake rejected: %w", cause)
		}, func(context.Context, net.Conn) { t.Fatal("unreachable serve") }, func() { t.Fatal("permanent failure retried") })
		if !errors.Is(err, cause) || attempts != 1 {
			t.Fatalf("err=%v attempts=%d", err, attempts)
		}
	}
}
func TestHostRetryRecoversWithoutReplaying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts, served, notices := 0, 0, 0
	err := reconnectHost(ctx, time.Millisecond, 4*time.Millisecond, func(context.Context) (net.Conn, error) {
		attempts++
		if attempts <= 2 {
			return nil, errors.New("temporary transport failure")
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}, func(context.Context, net.Conn) { served++; cancel() }, func() { notices++ })
	if !errors.Is(err, context.Canceled) || attempts != 3 || served != 1 || notices != 2 {
		t.Fatalf("err=%v attempts=%d served=%d notices=%d", err, attempts, served, notices)
	}
}
func TestRapidDisconnectUsesBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	started := time.Now()
	err := reconnectHost(ctx, 5*time.Millisecond, 20*time.Millisecond, func(context.Context) (net.Conn, error) { attempts++; a, b := net.Pipe(); b.Close(); return a, nil }, func(context.Context, net.Conn) {
		if attempts == 4 {
			cancel()
		}
	}, func() {})
	if !errors.Is(err, context.Canceled) || attempts != 4 {
		t.Fatal(err)
	}
	if time.Since(started) < 15*time.Millisecond {
		t.Fatal("rapid disconnects bypassed minimum backoff")
	}
}
func TestReconnectOptionValidation(t *testing.T) {
	for _, args := range [][]string{{"--reconnect-min", "0"}, {"--reconnect-min", "2s", "--reconnect-max", "1s"}, {"--reconnect-max", "25h"}} {
		if err := share(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid reconnect args accepted: %v", args)
		}
	}
}

func TestHostRetryTypedPermanentFailures(t *testing.T) {
	cases := []error{relay.ErrInvalidURL, relay.ErrRedirectDenied, relay.ErrHandshakeRejected, x509.UnknownAuthorityError{}, x509.HostnameError{Host: "example.test"}, x509.CertificateInvalidError{Reason: x509.Expired}, &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}
	for _, cause := range cases {
		attempts := 0
		wrapped := fmt.Errorf("transport rejected: %w", cause)
		err := reconnectHost(context.Background(), time.Millisecond, time.Second, func(context.Context) (net.Conn, error) { attempts++; return nil, wrapped }, func(context.Context, net.Conn) { t.Fatal("rejected transport served") }, func() { t.Fatal("permanent transport retried") })
		if err != wrapped || attempts != 1 {
			t.Fatalf("permanent error %T retried", cause)
		}
	}
	for _, err := range []error{relay.ErrUnavailable, &net.DNSError{IsTimeout: true}, syscall.ECONNREFUSED, context.DeadlineExceeded} {
		if permanentRelayError(err) {
			t.Fatalf("transient error %T classified permanent", err)
		}
	}
}
func TestHostRetryActualUntrustedTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted server was reached") }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	err := reconnectHost(ctx, time.Millisecond, time.Second, func(ctx context.Context) (net.Conn, error) {
		attempts++
		return relay.DialHost(ctx, server.URL, relay.Session{ID: "test", HostToken: "host"})
	}, func(context.Context, net.Conn) { t.Fatal("untrusted transport served") }, func() { t.Fatal("certificate failure retried") })
	var certificate *tls.CertificateVerificationError
	if !errors.As(err, &certificate) || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}
