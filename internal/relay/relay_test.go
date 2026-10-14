package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

const testKey = "0123456789abcdef0123456789abcdef"

func setup(t *testing.T, limit int) (*Server, string, Session) {
	t.Helper()
	srv, err := New(testKey, limit)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(srv)
	t.Cleanup(func() { srv.Close(); h.Close() })
	sess, err := Create(context.Background(), h.URL, testKey, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return srv, h.URL, sess
}
func pair(t *testing.T, base string, sess Session) (net.Conn, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	type result struct {
		c net.Conn
		e error
	}
	ch := make(chan result, 1)
	go func() { c, e := DialHost(ctx, base, sess); ch <- result{c, e} }()
	client, err := DialClient(ctx, base, sess)
	if err != nil {
		t.Fatal(err)
	}
	host := <-ch
	if host.e != nil {
		t.Fatal(host.e)
	}
	t.Cleanup(func() { host.c.Close(); client.Close() })
	return host.c, client
}
func transfer(t *testing.T, a, b net.Conn) {
	t.Helper()
	message := []byte("SSH-2.0-integration\r\n\x00\x01\xff")
	errCh := make(chan error, 1)
	go func() { _, err := a.Write(message); errCh <- err }()
	got := make([]byte, len(message))
	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(b, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(message) {
		t.Fatalf("corrupt transfer %q", got)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentPairs(t *testing.T) {
	_, base, sess := setup(t, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); a, b := pair(t, base, sess); transfer(t, a, b); transfer(t, b, a) }()
	}
	wg.Wait()
}
func TestRoleAuthentication(t *testing.T) {
	_, base, sess := setup(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wrong := sess
	wrong.HostToken = sess.ClientToken
	if c, err := DialHost(ctx, base, wrong); err == nil {
		c.Close()
		t.Fatal("wrong role accepted")
	}
	wrong = sess
	wrong.ClientToken = "invalid"
	if c, err := DialClient(ctx, base, wrong); err == nil {
		c.Close()
		t.Fatal("wrong token accepted")
	}
}
func TestRevokeClosesActive(t *testing.T) {
	_, base, sess := setup(t, 4)
	a, b := pair(t, base, sess)
	if err := Revoke(context.Background(), base, sess); err != nil {
		t.Fatal(err)
	}
	for _, c := range []net.Conn{a, b} {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("revoked connection remains alive")
		}
	}
}
func TestTTLClosesActiveAndWaiting(t *testing.T) {
	_, base, _ := setup(t, 4)
	sess, err := Create(context.Background(), base, testKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a, b := pair(t, base, sess)
	wait := make(chan error, 1)
	go func() {
		c, e := DialHost(context.Background(), base, sess)
		if c != nil {
			c.Close()
		}
		wait <- e
	}()
	for _, c := range []net.Conn{a, b} {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("expired connection alive")
		}
	}
	select {
	case e := <-wait:
		if e == nil {
			t.Fatal("expired waiter succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expired waiter remains")
	}
}
func TestCancellationAndCapacity(t *testing.T) {
	srv, base, sess := setup(t, 1)
	if _, err := Create(context.Background(), base, testKey, time.Minute); err == nil {
		t.Fatal("session limit not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		c, e := DialHost(ctx, base, sess)
		if c != nil {
			c.Close()
		}
		done <- e
	}()
	deadline := time.Now().Add(time.Second)
	for {
		srv.mu.Lock()
		n := srv.sessions[sess.ID].hosts
		srv.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter failed to connect")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancelled waiter succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close waiter")
	}
	deadline = time.Now().Add(time.Second)
	for {
		srv.mu.Lock()
		n := srv.sessions[sess.ID].hosts
		srv.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled waiter occupies capacity")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestURLSecurity(t *testing.T) {
	for _, base := range []string{"http://example.com", "ftp://localhost", "https://user:pass@example.com", "https://example.com?token=secret"} {
		if _, err := endpoint(base, "/v1/sessions"); err == nil {
			t.Fatalf("insecure URL accepted: %s", base)
		}
	}
}
func TestUnauthenticatedCreate(t *testing.T) {
	_, base, _ := setup(t, 4)
	resp, err := http.Post(base+"/v1/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPersistentOptInAndCustomTTL(t *testing.T) {
	srv, base, _ := setup(t, 4)
	if _, err := Create(context.Background(), base, testKey, 0); err == nil {
		t.Fatal("persistent allowed without opt-in")
	}
	srv.AllowPersistent = true
	sess, err := Create(context.Background(), base, testKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.ExpiresAt.IsZero() {
		t.Fatal("persistent session has deadline")
	}
	if _, err := Create(context.Background(), base, testKey, 48*time.Hour); err == nil {
		t.Fatal("default TTL limit bypassed")
	}
	srv.MaxTTL = 72 * time.Hour
	if _, err := Create(context.Background(), base, testKey, 48*time.Hour); err != nil {
		t.Fatal(err)
	}
}
func TestRevokeClosesWaiting(t *testing.T) {
	srv, base, sess := setup(t, 4)
	done := make(chan error, 1)
	go func() {
		c, e := DialHost(context.Background(), base, sess)
		if c != nil {
			c.Close()
		}
		done <- e
	}()
	deadline := time.Now().Add(time.Second)
	for {
		srv.mu.Lock()
		n := srv.sessions[sess.ID].hosts
		srv.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host did not connect")
		}
		time.Sleep(time.Millisecond)
	}
	if err := Revoke(context.Background(), base, sess); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("revoked waiter succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("revoked waiter stayed alive")
	}
}

func TestConnectionCapacity(t *testing.T) {
	srv, base, sess := setup(t, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, maxConnections)
	for range maxConnections {
		go func() {
			c, e := DialHost(ctx, base, sess)
			if c != nil {
				c.Close()
			}
			done <- e
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.mu.Lock()
		n := srv.sessions[sess.ID].hosts
		srv.mu.Unlock()
		if n == maxConnections {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiting connections not established")
		}
		time.Sleep(time.Millisecond)
	}
	limited, cancelLimited := context.WithTimeout(context.Background(), time.Second)
	defer cancelLimited()
	if c, e := DialHost(limited, base, sess); e == nil {
		c.Close()
		t.Fatal("role connection capacity not enforced")
	}
	cancel()
	for range maxConnections {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("waiter failed to cancel")
		}
	}
}

func TestDialPermanentHandshakeErrors(t *testing.T) {
	for _, entry := range []struct {
		status int
		want   error
	}{{404, ErrSessionClosed}, {401, ErrUnauthorized}, {403, ErrUnauthorized}, {502, nil}} {
		t.Run(strconv.Itoa(entry.status), func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(entry.status) }))
			defer h.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for _, dialer := range []func(context.Context, string, Session) (net.Conn, error){DialHost, DialClient} {
				c, err := dialer(ctx, h.URL, Session{ID: "test", HostToken: "host", ClientToken: "client"})
				if c != nil {
					c.Close()
				}
				if err == nil {
					t.Fatal("unexpected successful handshake")
				}
				if entry.want != nil && !errors.Is(err, entry.want) {
					t.Fatalf("error %v does not wrap %v", err, entry.want)
				}
				if entry.want == nil && (errors.Is(err, ErrSessionClosed) || errors.Is(err, ErrUnauthorized)) {
					t.Fatalf("temporary failure classified permanent: %v", err)
				}
			}
		})
	}
}
func TestRevokedSessionDialClosed(t *testing.T) {
	_, base, sess := setup(t, 4)
	if err := Revoke(context.Background(), base, sess); err != nil {
		t.Fatal(err)
	}
	_, err := DialHost(context.Background(), base, sess)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("revoked session error: %v", err)
	}
}
