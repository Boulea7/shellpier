package access

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/relay"
	"golang.org/x/crypto/ssh"
)

func tinyRetry() RetryOptions {
	return RetryOptions{Retries: 2, MinDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}
func TestRetryTransientRecoveryAndLimit(t *testing.T) {
	options := tinyRetry()
	calls := 0
	expected := &ssh.Client{}
	got, err := retryDial(context.Background(), options, func() (*ssh.Client, error) {
		calls++
		if calls < 3 {
			return nil, fmt.Errorf("dial: %w", syscall.ECONNREFUSED)
		}
		return expected, nil
	})
	if err != nil || got != expected || calls != 3 {
		t.Fatalf("recovery calls=%d err=%v", calls, err)
	}
	calls = 0
	_, err = retryDial(context.Background(), options, func() (*ssh.Client, error) { calls++; return nil, io.EOF })
	if err == nil || calls != 3 {
		t.Fatalf("limit calls=%d err=%v", calls, err)
	}
	options.Retries = 0
	calls = 0
	_, _ = retryDial(context.Background(), options, func() (*ssh.Client, error) { calls++; return nil, io.EOF })
	if calls != 1 {
		t.Fatal("disabled retries made extra attempts")
	}
}
func TestRetryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	options := tinyRetry()
	options.MinDelay = time.Second
	options.MaxDelay = time.Second
	calls := 0
	start := time.Now()
	_, err := retryDial(ctx, options, func() (*ssh.Client, error) { calls++; cancel(); return nil, io.EOF })
	if !errors.Is(err, context.Canceled) || calls != 1 || time.Since(start) > 100*time.Millisecond {
		t.Fatal("cancellation did not stop retries")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	calls = 0
	_, err = retryDial(ctx, options, func() (*ssh.Client, error) { calls++; return nil, io.EOF })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("deadline did not interrupt backoff")
	}
}
func TestRetryPermanentErrors(t *testing.T) {
	permanent := []error{relay.ErrSessionClosed, relay.ErrUnauthorized, relay.ErrInvalidURL, relay.ErrRedirectDenied, relay.ErrHandshakeRejected, x509.UnknownAuthorityError{}, errors.New("ssh: handshake failed: unable to authenticate"), errors.New("ssh: host key mismatch"), errors.New("invalid interface"), &net.DNSError{IsNotFound: true, Err: "not found"}, errors.Join(io.EOF, relay.ErrUnauthorized), fmt.Errorf("wrapped unavailable: %w", errors.Join(relay.ErrUnavailable, x509.UnknownAuthorityError{})), fmt.Errorf("wrapped: %w", errors.Join(io.EOF, errors.New("invalid policy")))}
	for i, err := range permanent {
		calls := 0
		_, got := retryDial(context.Background(), tinyRetry(), func() (*ssh.Client, error) { calls++; return nil, err })
		if got == nil || calls != 1 {
			t.Fatalf("permanent case %d retried %d times", i, calls)
		}
	}
}

func retryServer(t *testing.T, failFirst int, rejectAuth bool) (Ticket, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	ticket, host, err := NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(host.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ticket.Addresses = []string{ln.Addr().String()}
	attempts, commands := &atomic.Int32{}, &atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			count := int(attempts.Add(1))
			if count <= failFirst {
				conn.Close()
				continue
			}
			go func() {
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				defer conn.Close()
				config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
					if rejectAuth {
						return nil, errors.New("rejected")
					}
					return nil, nil
				}}
				config.AddHostKey(signer)
				sc, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(requests)
				for pending := range channels {
					if pending.ChannelType() != "session" {
						pending.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := pending.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type == "exec" {
								commands.Add(1)
								request.Reply(true, nil)
								io.WriteString(channel, "executed once")
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
								return
							}
							request.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); ln.Close(); <-done })
	return ticket, attempts, commands
}
func TestDialRetryRealRecoveryAndNoCommandReplay(t *testing.T) {
	ticket, attempts, commands := retryServer(t, 2, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := DialRetry(ctx, ticket, "", tinyRetry())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	out, err := session.Output("authorized command")
	if err != nil || string(out) != "executed once" || attempts.Load() != 3 || commands.Load() != 1 {
		t.Fatalf("attempts=%d commands=%d err=%v", attempts.Load(), commands.Load(), err)
	}
}
func TestDialRetryAuthenticationIsPermanent(t *testing.T) {
	ticket, attempts, _ := retryServer(t, 0, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := DialRetry(ctx, ticket, "", tinyRetry()); err == nil {
		t.Fatal("authentication unexpectedly succeeded")
	}
	if attempts.Load() != 1 {
		t.Fatal("authentication failure retried")
	}
}
func TestDialRetryHostKeyMismatchIsPermanent(t *testing.T) {
	ticket, attempts, _ := retryServer(t, 0, false)
	other, _, err := NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ticket.HostKey = other.HostKey
	if _, err = DialRetry(context.Background(), ticket, "", tinyRetry()); err == nil {
		t.Fatal("host key mismatch accepted")
	}
	if attempts.Load() != 1 {
		t.Fatal("host key mismatch retried")
	}
}
func TestDialRetryTTLStopsBackoff(t *testing.T) {
	ticket, attempts, _ := retryServer(t, 10, false)
	ticket.ExpiresAt = time.Now().Add(30 * time.Millisecond)
	options := RetryOptions{Retries: 10, MinDelay: time.Second, MaxDelay: time.Second}
	if _, err := DialRetry(context.Background(), ticket, "", options); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("TTL returned %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatal("TTL permitted another retry")
	}
}

func TestDialRetryInvalidOptionsAndTicketFailBeforeNetwork(t *testing.T) {
	ticket, attempts, _ := retryServer(t, 0, false)
	for _, options := range []RetryOptions{{Retries: -1, MinDelay: time.Millisecond, MaxDelay: time.Second}, {Retries: 11, MinDelay: time.Millisecond, MaxDelay: time.Second}, {Retries: 2, MinDelay: 0, MaxDelay: time.Second}, {Retries: 2, MinDelay: time.Second, MaxDelay: time.Millisecond}} {
		if _, err := DialRetry(context.Background(), ticket, "", options); err == nil {
			t.Fatal("invalid retry options accepted")
		}
	}
	ticket.PrivateKey = "invalid"
	if _, err := DialRetry(context.Background(), ticket, "", tinyRetry()); err == nil {
		t.Fatal("invalid ticket accepted")
	}
	if attempts.Load() != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestDialRetryRelayTransientHTTPThenPermanentAuth(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	ticket, _, err := NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ticket.RelayURL = server.URL
	ticket.RelayToken = strings.Repeat("x", 32)
	if _, err = DialRetry(context.Background(), ticket, "", tinyRetry()); !errors.Is(err, relay.ErrUnauthorized) {
		t.Fatalf("relay retry returned %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("relay temporary failure attempts=%d", attempts.Load())
	}
}
