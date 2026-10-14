// Package relay carries SSH bytes between authenticated outbound WebSockets.
package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ErrSessionClosed identifies a missing, expired, or revoked relay session.
var ErrSessionClosed = errors.New("relay session closed")

// ErrUnauthorized identifies a rejected role token or access policy.
var ErrUnauthorized = errors.New("relay unauthorized")

// ErrUnavailable identifies a transient HTTP capacity or upstream failure.
var ErrUnavailable = errors.New("relay temporarily unavailable")

// ErrInvalidURL identifies a relay address rejected before dialing.
var ErrInvalidURL = errors.New("invalid relay URL")

// ErrRedirectDenied identifies a redirect rejected to protect credentials.
var ErrRedirectDenied = errors.New("relay redirect denied")

// ErrHandshakeRejected identifies a nonretryable HTTP upgrade rejection.
var ErrHandshakeRejected = errors.New("relay WebSocket handshake rejected")

const maxConnections = 8
const maxMessage = 1 << 20

type Session struct {
	ID          string    `json:"id"`
	HostToken   string    `json:"host_token"`
	ClientToken string    `json:"client_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type state struct {
	Session
	ctx            context.Context
	cancel         context.CancelFunc
	host, client   []*peer
	hosts, clients int
}
type peer struct {
	ws       *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	incoming chan []byte
	paired   chan *peer
}
type Server struct {
	// AllowPersistent permits ttl_seconds=0. Configure before serving requests.
	AllowPersistent bool
	// MaxTTL defaults to 24 hours. Configure before serving requests.
	MaxTTL   time.Duration
	key      string
	limit    int
	mu       sync.Mutex
	sessions map[string]*state
	closed   bool
}

func New(apiKey string, maxSessions int) (*Server, error) {
	if len(apiKey) < 32 {
		return nil, errors.New("API key must contain at least 32 characters")
	}
	if maxSessions < 1 {
		return nil, errors.New("maxSessions must be positive")
	}
	return &Server{key: apiKey, limit: maxSessions, sessions: make(map[string]*state)}, nil
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func bearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for id, v := range s.sessions {
		v.cancel()
		delete(s.sessions, id)
	}
	return nil
}
func (s *Server) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.sessions[id]; v != nil {
		v.cancel()
		delete(s.sessions, id)
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.URL.Path == "/v1/sessions" && r.Method == http.MethodPost {
		if !equal(bearer(r), s.key) {
			http.Error(w, "unauthorized", 401)
			return
		}
		var input struct {
			TTL int `json:"ttl_seconds"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		maxTTL := s.MaxTTL
		if maxTTL <= 0 {
			maxTTL = 24 * time.Hour
		}
		if d.Decode(&input) != nil || input.TTL < 0 || (input.TTL == 0 && !s.AllowPersistent) || int64(input.TTL) > int64(maxTTL/time.Second) {
			http.Error(w, "ttl_seconds outside allowed range", 400)
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			http.Error(w, "invalid body", 400)
			return
		}
		id, e := randomToken()
		if e != nil {
			http.Error(w, "random source failed", 500)
			return
		}
		h, e := randomToken()
		if e != nil {
			http.Error(w, "random source failed", 500)
			return
		}
		c, e := randomToken()
		if e != nil {
			http.Error(w, "random source failed", 500)
			return
		}
		sess := Session{ID: id, HostToken: h, ClientToken: c}
		var ctx context.Context
		var cancel context.CancelFunc
		if input.TTL == 0 {
			ctx, cancel = context.WithCancel(context.Background())
		} else {
			sess.ExpiresAt = time.Now().Add(time.Duration(input.TTL) * time.Second)
			ctx, cancel = context.WithDeadline(context.Background(), sess.ExpiresAt)
		}
		v := &state{Session: sess, ctx: ctx, cancel: cancel}
		s.mu.Lock()
		if s.closed || len(s.sessions) >= s.limit {
			s.mu.Unlock()
			cancel()
			http.Error(w, "capacity unavailable", 503)
			return
		}
		s.sessions[id] = v
		s.mu.Unlock()
		go func() { <-ctx.Done(); s.remove(id) }()
		writeJSON(w, 201, sess)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/sessions/") && r.Method == http.MethodDelete {
		id := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
		s.mu.Lock()
		v := s.sessions[id]
		if v == nil {
			s.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		if !equal(bearer(r), s.key) && !equal(bearer(r), v.HostToken) {
			s.mu.Unlock()
			http.Error(w, "unauthorized", 401)
			return
		}
		v.cancel()
		delete(s.sessions, id)
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	role := ""
	id := ""
	if strings.HasPrefix(r.URL.Path, "/v1/host/") {
		role = "host"
		id = strings.TrimPrefix(r.URL.Path, "/v1/host/")
	}
	if strings.HasPrefix(r.URL.Path, "/v1/connect/") {
		role = "client"
		id = strings.TrimPrefix(r.URL.Path, "/v1/connect/")
	}
	if role == "" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	v := s.sessions[id]
	if v == nil || v.ctx.Err() != nil {
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	token := v.ClientToken
	if role == "host" {
		token = v.HostToken
	}
	if !equal(bearer(r), token) {
		s.mu.Unlock()
		http.Error(w, "unauthorized", 401)
		return
	}
	count := &v.clients
	if role == "host" {
		count = &v.hosts
	}
	if *count >= maxConnections {
		s.mu.Unlock()
		http.Error(w, "connection capacity unavailable", 429)
		return
	}
	*count++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); *count--; s.mu.Unlock() }()
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(maxMessage)
	ctx, cancel := context.WithCancel(v.ctx)
	p := &peer{ws: ws, ctx: ctx, cancel: cancel, incoming: make(chan []byte, 1), paired: make(chan *peer, 1)}
	defer cancel()
	defer ws.CloseNow()
	go func() { <-ctx.Done(); _ = ws.CloseNow() }()
	go func() {
		defer cancel()
		for {
			kind, data, err := ws.Read(ctx)
			if err != nil || kind != websocket.MessageBinary {
				return
			}
			select {
			case p.incoming <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pingCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	s.mu.Lock()
	own, other := &v.client, &v.host
	if role == "host" {
		own, other = &v.host, &v.client
	}
	var partner *peer
	for len(*other) > 0 {
		candidate := (*other)[0]
		*other = (*other)[1:]
		if candidate.ctx.Err() == nil {
			partner = candidate
			break
		}
	}
	if partner == nil {
		*own = append(*own, p)
	} else {
		partner.paired <- p
		p.paired <- partner
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		for i, c := range *own {
			if c == p {
				*own = append((*own)[:i], (*own)[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()
	select {
	case partner = <-p.paired:
	case <-ctx.Done():
		return
	}
	defer partner.cancel()
	writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	err = ws.Write(writeCtx, websocket.MessageText, []byte(`{"ready":true}`))
	stop()
	if err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-partner.ctx.Done():
			return
		case data := <-partner.incoming:
			writeCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			err := ws.Write(writeCtx, websocket.MessageBinary, data)
			stop()
			if err != nil {
				return
			}
		}
	}
}

func endpoint(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("%w: invalid address syntax", ErrInvalidURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: userinfo, query and fragment are not allowed", ErrInvalidURL)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return "", fmt.Errorf("%w: HTTPS required (HTTP allowed only on loopback)", ErrInvalidURL)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	return u.String(), nil
}
func Create(ctx context.Context, baseURL, apiKey string, ttl time.Duration) (Session, error) {
	var sess Session
	target, err := endpoint(baseURL, "/v1/sessions")
	if err != nil {
		return sess, err
	}
	seconds := int64(ttl / time.Second)
	if ttl < 0 || (ttl > 0 && seconds < 1) {
		return sess, errors.New("TTL must be zero or at least 1 second")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", target, strings.NewReader(fmt.Sprintf(`{"ttl_seconds":%d}`, seconds)))
	if err != nil {
		return sess, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := relayHTTPClient().Do(req)
	if err != nil {
		return sess, safeNetworkError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return sess, fmt.Errorf("create session: HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&sess)
	return sess, err
}
func Revoke(ctx context.Context, baseURL string, session Session) error {
	target, err := endpoint(baseURL, "/v1/sessions/"+url.PathEscape(session.ID))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "DELETE", target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+session.HostToken)
	resp, err := relayHTTPClient().Do(req)
	if err != nil {
		return safeNetworkError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 && resp.StatusCode != 404 {
		return fmt.Errorf("revoke session: HTTP %d", resp.StatusCode)
	}
	return nil
}
func DialHost(ctx context.Context, baseURL string, session Session) (net.Conn, error) {
	return dial(ctx, baseURL, "host", session.ID, session.HostToken)
}
func DialClient(ctx context.Context, baseURL string, session Session) (net.Conn, error) {
	return dial(ctx, baseURL, "connect", session.ID, session.ClientToken)
}

type managedConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *managedConn) Close() error { c.cancel(); return c.Conn.Close() }

func dial(ctx context.Context, baseURL, role, id, token string) (net.Conn, error) {
	return dialWithReadyTimeout(ctx, baseURL, role, id, token, 15*time.Second)
}
func dialWithReadyTimeout(ctx context.Context, baseURL, role, id, token string, readyTimeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()
	target, err := endpoint(baseURL, "/v1/"+role+"/"+url.PathEscape(id))
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(target)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	handshakeCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	ws, response, err := websocket.Dial(handshakeCtx, u.String(), &websocket.DialOptions{HTTPHeader: h, HTTPClient: relayHTTPClient()})
	stop()
	if err != nil {
		if response != nil {
			switch response.StatusCode {
			case http.StatusNotFound:
				return nil, fmt.Errorf("%w: WebSocket handshake HTTP %d", ErrSessionClosed, response.StatusCode)
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, fmt.Errorf("%w: WebSocket handshake HTTP %d", ErrUnauthorized, response.StatusCode)
			case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
				return nil, fmt.Errorf("%w: WebSocket handshake HTTP %d", ErrUnavailable, response.StatusCode)
			default:
				if response.StatusCode >= 300 && response.StatusCode < 400 {
					return nil, fmt.Errorf("%w: WebSocket handshake HTTP %d", ErrRedirectDenied, response.StatusCode)
				}
				return nil, fmt.Errorf("%w: HTTP %d", ErrHandshakeRejected, response.StatusCode)
			}
		}
		return nil, safeNetworkError{err}
	}
	ws.SetReadLimit(maxMessage)
	readCtx := ctx
	readStop := func() {}
	if role == "connect" {
		readCtx, readStop = context.WithTimeout(ctx, readyTimeout)
	}
	kind, data, err := ws.Read(readCtx)
	readStop()
	if err != nil {
		_ = ws.CloseNow()
		return nil, err
	}
	var ready struct {
		Ready bool `json:"ready"`
	}
	if kind != websocket.MessageText || json.Unmarshal(data, &ready) != nil || !ready.Ready {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("%w: invalid readiness response", ErrHandshakeRejected)
	}
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	go func() { <-ctx.Done(); _ = ws.CloseNow() }()
	success = true
	return &managedConn{Conn: conn, cancel: cancel}, nil
}

// Preserve custom transports and root CAs while preventing credential-bearing
// requests from following redirects, including HTTPS-to-HTTP redirects.
func relayHTTPClient() *http.Client {
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

var credentialURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s]*@`)

type safeNetworkError struct{ err error }

func (e safeNetworkError) Error() string {
	return credentialURL.ReplaceAllString(e.err.Error(), `${1}[redacted]@`)
}
func (e safeNetworkError) Unwrap() error { return e.err }
