package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/relay"
	"github.com/pkg/sftp"
)

type eventOutput struct {
	mu      sync.Mutex
	pending []byte
	ready   chan string
}

func (w *eventOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		var event struct {
			Event string `json:"event"`
			State string `json:"state"`
		}
		json.Unmarshal(w.pending[:index], &event)
		w.pending = w.pending[index+1:]
		if event.Event == "session_created" {
			select {
			case w.ready <- event.State:
			default:
			}
		}
	}
	return len(p), nil
}
func startHost(t *testing.T, args ...string) (string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := &eventOutput{ready: make(chan string, 1)}
	done := make(chan error, 1)
	completed := make(chan struct{})
	go func() { defer close(completed); done <- Run(ctx, args, strings.NewReader(""), out, io.Discard, "test") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-completed:
		case <-time.After(7 * time.Second):
			t.Error("background host did not clean up")
		}
	})
	select {
	case state := <-out.ready:
		return state, done
	case e := <-done:
		t.Fatalf("host start failed: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("host did not announce readiness")
	}
	return "", done
}
func invoke(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	if e := Run(ctx, args, strings.NewReader(""), &out, io.Discard, "test"); e != nil {
		t.Fatalf("command %s failed: %v", args[0], e)
	}
	return out.String()
}
func awaitHost(t *testing.T, done <-chan error, state string) {
	t.Helper()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("host cleanup failed: %v", e)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("host did not terminate")
	}
	if _, e := os.Stat(state); !os.IsNotExist(e) {
		t.Fatal("runtime state was not removed")
	}
}
func outputCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Write('中文链路')"
	}
	return "printf '中文链路'"
}
func assertExecSFTP(t *testing.T, ticket access.Ticket, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, e := access.Dial(ctx, ticket, "")
	if e != nil {
		t.Fatalf("SSH dial failed: %v", e)
	}
	defer c.Close()
	s, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	out, e := s.Output(outputCommand())
	s.Close()
	if e != nil || string(out) != "中文链路" {
		t.Fatalf("SSH command output mismatch: %v", e)
	}
	sf, e := sftp.NewClient(c)
	if e != nil {
		t.Fatal(e)
	}
	defer sf.Close()
	cwd, e := sf.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	name := path.Join(cwd, "传输.txt")
	if runtime.GOOS == "windows" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	// Relative creation and protocol-absolute read must address the same host file.
	f, e := sf.Create("传输.txt")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.Write([]byte("私有传输"))
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	f, e = sf.Open(name)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	f.Close()
	if e != nil || string(data) != "私有传输" {
		t.Fatal("SFTP roundtrip mismatch")
	}
	if actual, e := os.ReadFile(filepath.Join(dir, "传输.txt")); e != nil || string(actual) != "私有传输" {
		t.Fatal("relative and protocol-absolute SFTP paths did not resolve to the configured work directory")
	}
}

func TestDirectShareTTL(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "access.json")
	state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "2s", "--output", path, "--json", "--work-dir", dir)
	ticket, e := access.Read("@" + path)
	if e != nil {
		t.Fatal(e)
	}
	// TTL coverage should not depend on cold shell startup performance.
	c, e := access.Dial(context.Background(), ticket, "")
	if e != nil {
		t.Fatal("short-lived SSH handshake failed")
	}
	c.Close()
	var status struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(invoke(t, "status", "--state", state)), &status) != nil || status.Status != "running" {
		t.Fatal("status did not show running host")
	}
	awaitHost(t, done, state)
	if _, e = access.Dial(context.Background(), ticket, ""); e == nil {
		t.Fatal("expired ticket connected")
	}
	// Prove the listener itself closed, independently of ticket expiry validation.
	conn, e := net.DialTimeout("tcp", ticket.Addresses[0], time.Second)
	if e == nil {
		conn.Close()
		t.Fatal("expired listener still accepted TCP")
	}
}
func TestDirectStopAndConfigCleanup(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "ticket.json")
	state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--output", path, "--json", "--work-dir", dir)
	ticket, e := access.Read("@" + path)
	if e != nil {
		t.Fatal(e)
	}
	assertExecSFTP(t, ticket, dir)
	invoke(t, "status", "--state", state)
	configDir := filepath.Join(dir, "generated")
	invoke(t, "ssh-config", "@"+path, "--out", configDir)
	unrelated := filepath.Join(configDir, "notes.txt")
	if e := os.WriteFile(unrelated, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	invoke(t, "stop", "--state", state)
	awaitHost(t, done, state)
	invoke(t, "cleanup", "--dir", configDir)
	data, e := os.ReadFile(unrelated)
	if e != nil || string(data) != "preserve" {
		t.Fatal("cleanup damaged unrelated file")
	}
	for _, name := range []string{"access.json", "identity", "known_hosts", "ssh_config", "shellpier-manifest.json"} {
		if _, e := os.Stat(filepath.Join(configDir, name)); !os.IsNotExist(e) {
			t.Fatal("generated credentials remain")
		}
	}
}
func TestRelayInviteHostConnectRevoke(t *testing.T) {
	key := strings.Repeat("k", 64)
	t.Setenv("SHELLPIER_RELAY_KEY", key)
	rs, e := relay.New(key, 16)
	if e != nil {
		t.Fatal(e)
	}
	defer rs.Close()
	httpServer := httptest.NewServer(rs)
	defer httpServer.Close()
	dir := t.TempDir()
	generated := filepath.Join(dir, "invite")
	invoke(t, "invite", "--relay", httpServer.URL, "--ttl", "30s", "--out", generated)
	a, e := access.Read("@" + filepath.Join(generated, "access.json"))
	if e != nil {
		t.Fatal(e)
	}
	h, e := access.Read("@" + filepath.Join(generated, "host.ticket"))
	if e != nil {
		t.Fatal(e)
	}
	if h.PrivateKey != "" || h.HostPrivateKey == "" || a.HostPrivateKey != "" || a.PrivateKey == "" || a.RelayToken == h.RelayToken {
		t.Fatal("invite role credentials were not separated")
	}
	identity, e := os.ReadFile(filepath.Join(generated, "identity"))
	if e != nil {
		t.Fatal(e)
	}
	if string(identity) != a.PrivateKey {
		t.Fatal("operator identity mismatch")
	}
	for _, name := range []string{"host.ticket", "access.json", "ssh_config", "known_hosts", "identity"} {
		if _, e := os.Stat(filepath.Join(generated, name)); e != nil {
			t.Fatal("invite output missing")
		}
	}
	state, done := startHost(t, "host", "--json", "--work-dir", dir, "@"+filepath.Join(generated, "host.ticket"))
	assertExecSFTP(t, a, dir)
	if strings.TrimSpace(invoke(t, "connect", "@"+filepath.Join(generated, "access.json"), "--", "whoami")) == "" {
		t.Fatal("whoami returned no identity")
	}
	if invoke(t, "connect", "@"+filepath.Join(generated, "access.json"), "--", outputCommand()) != "中文链路" {
		t.Fatal("connect output mismatch")
	}
	invoke(t, "revoke", "@"+filepath.Join(generated, "host.ticket"))
	awaitHost(t, done, state)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, e := access.Dial(ctx, a, ""); e == nil {
		c.Close()
		t.Fatal("revoked relay accepted connection")
	}
	invoke(t, "cleanup", "--dir", generated)
	if _, e := os.Stat(generated); !os.IsNotExist(e) {
		t.Fatal("invite cleanup left generated directory")
	}
}
func TestInvalidCLIInputsAndPaths(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	t.Setenv("SHELLPIER_RELAY_KEY", "")
	dir := t.TempDir()
	file := filepath.Join(dir, "existing")
	if e := os.WriteFile(file, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	cases := [][]string{{"unknown"}, {"connect"}, {"host"}, {"share", "--ttl", "-1s"}, {"share", "--ttl", "50ms"}, {"share", "--shell", "invalid"}, {"share", "--work-dir", file}, {"share", "--listen", "127.0.0.1:0", "--output", file}, {"cleanup", "--dir", dir}, {"cleanup", "--dir", file}, {"relay", "--init-key", file}, {"invite", "--relay", "http://127.0.0.1:1", "--out", filepath.Join(dir, "invite")}, {"status", "--state", file}, {"stop", "--state", dir}, {"revoke", file}}
	for i, args := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e := Run(ctx, args, strings.NewReader(""), io.Discard, io.Discard, "test")
		cancel()
		if e == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
	if data, e := os.ReadFile(file); e != nil || string(data) != "preserve" {
		t.Fatal("invalid command overwrote existing file")
	}
	a, _, e := access.NewPair(time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	a.Addresses = []string{"127.0.0.1:1"}
	a.ExpiresAt = time.Now().Add(-time.Second)
	raw, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	expired := filepath.Join(dir, "expired.json")
	if e = os.WriteFile(expired, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = Run(context.Background(), []string{"connect", "@" + expired}, nil, io.Discard, io.Discard, "test"); e == nil {
		t.Fatal("expired ticket accepted")
	}
}

func TestCleanupRejectsTraversalManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "generated")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	unrelated := filepath.Join(root, "unrelated")
	if e := os.WriteFile(unrelated, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	forged := []byte(`{"version":1,"id":"a-session","files":["../unrelated","identity","known_hosts","ssh_config"]}`)
	if e := os.WriteFile(filepath.Join(dir, "shellpier-manifest.json"), forged, 0600); e != nil {
		t.Fatal(e)
	}
	if e := Run(context.Background(), []string{"cleanup", "--dir", dir}, nil, io.Discard, io.Discard, "test"); e == nil {
		t.Fatal("traversal manifest accepted")
	}
	if data, e := os.ReadFile(unrelated); e != nil || string(data) != "preserve" {
		t.Fatal("cleanup escaped its directory")
	}
}

func TestRelayDisconnectDoesNotReplayCommand(t *testing.T) {
	key := strings.Repeat("d", 64)
	t.Setenv("SHELLPIER_RELAY_KEY", key)
	rs, e := relay.New(key, 16)
	if e != nil {
		t.Fatal(e)
	}
	defer rs.Close()
	var mu sync.Mutex
	var hijacked []net.Conn
	server := httptest.NewUnstartedServer(rs)
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			mu.Lock()
			hijacked = append(hijacked, c)
			mu.Unlock()
		}
	}
	server.Start()
	defer server.Close()
	dir := t.TempDir()
	generated := filepath.Join(dir, "invite")
	invoke(t, "invite", "--relay", server.URL, "--ttl", "30s", "--out", generated)
	state, done := startHost(t, "host", "--json", "--work-dir", dir, "@"+filepath.Join(generated, "host.ticket"))
	ticket, e := access.Read("@" + filepath.Join(generated, "access.json"))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, e := access.Dial(ctx, ticket, "")
	if e != nil {
		t.Fatal("relay SSH dial failed")
	}
	defer c.Close()
	session, e := c.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	command := "printf 'begun\\n' >> run-marker; sleep 30"
	if runtime.GOOS == "windows" {
		command = "Add-Content -LiteralPath run-marker -Value begun; Start-Sleep -Seconds 30"
	}
	if e = session.Start(command); e != nil {
		t.Fatal("long command failed to start")
	}
	marker := filepath.Join(dir, "run-marker")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b, e := os.ReadFile(marker); e == nil && strings.Contains(string(b), "begun") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("long command did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	connections := append([]net.Conn(nil), hijacked...)
	mu.Unlock()
	for _, conn := range connections {
		conn.Close()
	}
	interrupted := make(chan error, 1)
	go func() { interrupted <- session.Wait() }()
	select {
	case e := <-interrupted:
		if e == nil {
			t.Fatal("interrupted command reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected command did not terminate")
	}
	// A new command can use a repaired transport, while the interrupted command stays stopped.
	if invoke(t, "connect", "@"+filepath.Join(generated, "access.json"), "--", outputCommand()) != "中文链路" {
		t.Fatal("relay did not reconnect")
	}
	b, e := os.ReadFile(marker)
	if e != nil || strings.Count(string(b), "begun") != 1 {
		t.Fatal("interrupted command was replayed")
	}
	invoke(t, "stop", "--state", state)
	awaitHost(t, done, state)
}
