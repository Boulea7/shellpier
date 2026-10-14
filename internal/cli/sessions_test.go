package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
)

func writeSessionFixture(t *testing.T, root string, state localState) string {
	t.Helper()
	dir, err := os.MkdirTemp(root, "shellpier-")
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := writeJSONPrivate(path, state); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionsLiveStopAndStale(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("TMPDIR", runtimeDir)
	t.Setenv("TMP", runtimeDir)
	t.Setenv("TEMP", runtimeDir)
	statePath, done := startHost(t, "share", "--json", "--relay=", "--listen", "127.0.0.1:0", "--ttl", "1m", "--output", filepath.Join(t.TempDir(), "access.json"))
	var out bytes.Buffer
	if err := sessions(context.Background(), []string{"--json", "--dir", filepath.Dir(filepath.Dir(statePath))}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Sessions []sessionEntry `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list.Sessions {
		if s.StatePath == statePath {
			found = true
			if !s.Alive || s.Stale || s.Transport != "direct" {
				t.Fatalf("unexpected session: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("live session missing")
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := control(context.Background(), "stop", []string{"--state", statePath, "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("host did not stop")
	}
	root := t.TempDir()
	var state localState
	json.Unmarshal(b, &state)
	stalePath := writeSessionFixture(t, root, state)
	out.Reset()
	if err := sessions(context.Background(), []string{"--json", "--dir", root}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), state.Secret) || strings.Contains(out.String(), "control_url") {
		t.Fatal("session discovery leaked credentials")
	}
	if err := json.Unmarshal(out.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || !list.Sessions[0].Stale || list.Sessions[0].Alive {
		t.Fatalf("unexpected stale list: %s", out.String())
	}
	if _, err := os.Stat(stalePath); err != nil {
		t.Fatal("stale state deleted")
	}
}

func TestSessionsRejectURLsAndUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	valid := localState{Version: 1, Kind: "shellpier-host", ID: "test_session_id_123", Secret: strings.Repeat("a", 64), ControlURL: "http://127.0.0.1:9", Transport: "direct"}
	for _, address := range []string{"http://127.0.0.2:9", "http://localhost:9", "http://127.0.0.1:9/path", "http://user:password@127.0.0.1:9", "http://127.0.0.1:9?token=secret", "https://127.0.0.1:9", "http://127.0.0.1:9#fragment"} {
		state := valid
		state.ControlURL = address
		writeSessionFixture(t, root, state)
	}
	dir, err := os.MkdirTemp(root, "shellpier-")
	if err != nil {
		t.Fatal(err)
	}
	platform.SecureDir(dir)
	secretFile := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secretFile, []byte("credential-secret"), 0600)
	if err := os.Symlink(secretFile, filepath.Join(dir, "state.json")); err != nil {
		t.Log("symlink unavailable:", err)
	}
	largeDir, err := os.MkdirTemp(root, "shellpier-")
	if err != nil {
		t.Fatal(err)
	}
	platform.SecureDir(largeDir)
	if err := writePrivate(filepath.Join(largeDir, "state.json"), []byte(strings.Repeat("oversized-secret", 1000))); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := sessions(context.Background(), []string{"--json", "--dir", root}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "password") || strings.Contains(out.String(), "credential-secret") || strings.Contains(out.String(), valid.Secret) {
		t.Fatal("unsafe input leaked")
	}
	var list struct {
		Sessions []sessionEntry `json:"sessions"`
	}
	json.Unmarshal(out.Bytes(), &list)
	for _, item := range list.Sessions {
		if item.Alive || item.Error == "" {
			t.Fatalf("unsafe state accepted: %+v", item)
		}
	}
}

func TestSessionsTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	root := t.TempDir()
	writeSessionFixture(t, root, localState{Version: 1, Kind: "shellpier-host", ID: "slow_session_id_123", Secret: strings.Repeat("a", 64), ControlURL: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := sessions(ctx, []string{"--json", "--dir", root}, io.Discard, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation too slow")
	}
	start = time.Now()
	if err := sessions(context.Background(), []string{"--json", "--dir", root}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("probe timeout not bounded")
	}
}

func TestRelayInitKeyJSONDoesNotLeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	var out bytes.Buffer
	if err := runRelay(context.Background(), []string{"--json", "--init-key", path}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), strings.TrimSpace(string(b))) {
		t.Fatal("key leaked")
	}
	var metadata map[string]any
	if err := json.Unmarshal(out.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["path"] != path {
		t.Fatal("missing key path")
	}
}

func TestHostJSONLifecycleAndStatusTransport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := share(ctx, []string{"--json", "--relay=", "--listen", "127.0.0.1:0", "--ttl", "1s", "--output", filepath.Join(root, "access.json")}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	seen := map[string]bool{}
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		name, _ := event["event"].(string)
		seen[name] = true
		if name == "ready" || name == "closed" {
			if event["timestamp"] == nil || event["reason"] == nil || event["transport"] != "direct" {
				t.Fatalf("incomplete lifecycle metadata: %v", event)
			}
		}
	}
	if !seen["session_created"] || !seen["ready"] || !seen["closed"] {
		t.Fatalf("missing lifecycle events: %s", out.String())
	}
	for _, value := range []string{"secret", "private_key", "relay_token", "ticket"} {
		if strings.Contains(out.String(), `"`+value+`"`) {
			t.Fatalf("unexpected secret field %s", value)
		}
	}
	if relayConnectionState("relay", 1, true) != "connected" || relayConnectionState("relay", 0, true) != "reconnecting" || relayConnectionState("relay", 0, false) != "waiting" || relayConnectionState("direct", 0, false) != "not_applicable" {
		t.Fatal("incorrect connection state")
	}
}

func TestSessionsScansPastUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1100; i++ {
		if err := os.WriteFile(filepath.Join(root, strconv.Itoa(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture := writeSessionFixture(t, root, localState{Version: 1, Kind: "shellpier-host", ID: "fixture_session_123", Secret: strings.Repeat("a", 64), ControlURL: "http://127.0.0.1:9"})
	var out bytes.Buffer
	if err := sessions(context.Background(), []string{"--json", "--dir", root}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Truncated bool           `json:"truncated"`
		Sessions  []sessionEntry `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Truncated || len(result.Sessions) != 1 || result.Sessions[0].StatePath != fixture {
		t.Fatalf("missed fixture after unrelated entries: %s", out.String())
	}
}

func TestSessionsCandidateLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 1001; i++ {
		dir, err := os.MkdirTemp(root, "shellpier-")
		if err != nil {
			t.Fatal(err)
		}
		if err := platform.SecureDir(dir); err != nil {
			t.Fatal(err)
		}
		if err := writePrivate(filepath.Join(dir, "state.json"), []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := sessions(context.Background(), []string{"--json", "--dir", root}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Truncated bool           `json:"truncated"`
		Sessions  []sessionEntry `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Sessions) != 1000 {
		t.Fatalf("unexpected scan bound: %d truncated=%v", len(result.Sessions), result.Truncated)
	}
}
