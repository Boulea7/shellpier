package relay

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
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testDefaultTransport(t *testing.T, transport *http.Transport) {
	t.Helper()
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transport}
	t.Cleanup(func() { http.DefaultClient = old; transport.CloseIdleConnections() })
}
func trustedTransport(server *httptest.Server) *http.Transport {
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
}
func tlsRelay(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	srv, err := New(testKey, 8)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewTLSServer(srv)
	t.Cleanup(func() { srv.Close(); h.Close() })
	return h, srv
}
func TestTLSUntrustedRejected(t *testing.T) {
	h, _ := tlsRelay(t)
	testDefaultTransport(t, &http.Transport{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := Create(ctx, h.URL, testKey, time.Minute)
	if err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("unexpected TLS error: %v", err)
	}
}
func TestTrustedWSSBytes(t *testing.T) {
	h, _ := tlsRelay(t)
	testDefaultTransport(t, trustedTransport(h))
	sess, err := Create(context.Background(), h.URL, testKey, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	host, client := pair(t, h.URL, sess)
	transfer(t, host, client)
	transfer(t, client, host)
	if err := Revoke(context.Background(), h.URL, sess); err != nil {
		t.Fatal(err)
	}
}
func TestRedirectNeverForwardsCredentials(t *testing.T) {
	var received atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(200) }))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	testDefaultTransport(t, trustedTransport(source))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sess := Session{ID: "sample", HostToken: "private-host", ClientToken: "private-client"}
	if _, err := Create(ctx, source.URL, testKey, time.Minute); err == nil {
		t.Fatal("create accepted redirect")
	}
	if err := Revoke(ctx, source.URL, sess); err == nil {
		t.Fatal("revoke accepted redirect")
	}
	for _, dial := range []func(context.Context, string, Session) (net.Conn, error){DialHost, DialClient} {
		if c, err := dial(ctx, source.URL, sess); err == nil {
			c.Close()
			t.Fatal("WebSocket followed redirect")
		}
	}
	if received.Load() != 0 {
		t.Fatal("credential-bearing redirect reached destination")
	}
}

func connectProxy(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 405)
			return
		}
		count.Add(1)
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "upstream unavailable", 502)
			return
		}
		downstream, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = fmt.Fprint(buf, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buf.Flush(); err != nil {
			upstream.Close()
			downstream.Close()
			return
		}
		go func() { defer upstream.Close(); defer downstream.Close(); _, _ = io.Copy(upstream, buf) }()
		go func() { defer upstream.Close(); defer downstream.Close(); _, _ = io.Copy(downstream, upstream) }()
	}))
	t.Cleanup(proxy.Close)
	return proxy, count
}
func TestWSSViaCONNECTProxy(t *testing.T) {
	h, _ := tlsRelay(t)
	proxy, count := connectProxy(t, 200)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := trustedTransport(h)
	transport.Proxy = http.ProxyURL(proxyURL)
	testDefaultTransport(t, transport)
	sess, err := Create(context.Background(), h.URL, testKey, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a, b := pair(t, h.URL, sess)
	transfer(t, a, b)
	transfer(t, b, a)
	if count.Load() < 3 {
		t.Fatalf("expected HTTPS plus two WSS tunnels, got %d", count.Load())
	}
	if err := Revoke(context.Background(), h.URL, sess); err != nil {
		t.Fatal(err)
	}
}
func TestCONNECTProxyRejected(t *testing.T) {
	h, _ := tlsRelay(t)
	proxy, _ := connectProxy(t, 407)
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.User = url.UserPassword("private-proxy-user", "private-proxy-password")
	transport := trustedTransport(h)
	transport.Proxy = http.ProxyURL(proxyURL)
	testDefaultTransport(t, transport)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := Create(ctx, h.URL, testKey, time.Minute)
	if err == nil {
		t.Fatal("proxy denial ignored")
	}
	if strings.Contains(err.Error(), "private-proxy") {
		t.Fatal("proxy credentials leaked")
	}
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrSessionClosed) {
		t.Fatal("proxy error misclassified as session error")
	}
	for _, dial := range []func(context.Context, string, Session) (net.Conn, error){DialHost, DialClient} {
		c, e := dial(ctx, h.URL, Session{ID: "test", HostToken: "host", ClientToken: "client"})
		if c != nil {
			c.Close()
		}
		if e == nil {
			t.Fatal("WSS proxy denial ignored")
		}
		if strings.Contains(e.Error(), "private-proxy") {
			t.Fatal("WSS proxy credentials leaked")
		}
		if errors.Is(e, ErrUnauthorized) || errors.Is(e, ErrSessionClosed) {
			t.Fatal("WSS proxy denial misclassified as session error")
		}
	}
}
func TestUpgradeRejected(t *testing.T) {
	h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "upgrade disabled", 426) }))
	defer h.Close()
	testDefaultTransport(t, trustedTransport(h))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := DialHost(ctx, h.URL, Session{ID: "test", HostToken: "token"})
	if err == nil {
		t.Fatal("upgrade rejection ignored")
	}
	if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrSessionClosed) {
		t.Fatal("upgrade error misclassified")
	}
}
func TestNetworkErrorRedactionPreservesCause(t *testing.T) {
	cause := fmt.Errorf("proxy http://private-user:private-password@proxy.example:8080 unavailable: %w", context.DeadlineExceeded)
	err := safeNetworkError{cause}
	if strings.Contains(err.Error(), "private-user") || strings.Contains(err.Error(), "private-password") {
		t.Fatal("credentials leaked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("error cause lost")
	}
}

func TestClientReadinessTimeoutWithoutHost(t *testing.T) {
	_, base, sess := setup(t, 4)
	started := time.Now()
	c, err := dialWithReadyTimeout(context.Background(), base, "connect", sess.ID, sess.ClientToken, 30*time.Millisecond)
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected readiness deadline, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("client waited beyond readiness deadline")
	}
}
func TestHostReadinessUsesSessionContext(t *testing.T) {
	_, base, sess := setup(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	c, err := dialWithReadyTimeout(ctx, base, "host", sess.ID, sess.HostToken, time.Millisecond)
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected parent deadline, got %v", err)
	}
	if time.Since(started) < 40*time.Millisecond {
		t.Fatal("host incorrectly uses client readiness timeout")
	}
}
