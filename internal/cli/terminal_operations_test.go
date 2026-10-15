package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTerminalOperationCacheBoundsAndConflict(t *testing.T) {
	r := &terminalRuntime{}
	e, cached, err := r.beginOperation("request", []byte("a"))
	if err != nil || cached {
		t.Fatal(err)
	}
	if _, cached, err = r.beginOperation("request", []byte("a")); err != nil || !cached {
		t.Fatal("duplicate not cached")
	}
	if _, _, err = r.beginOperation("request", []byte("b")); err == nil {
		t.Fatal("content conflict accepted")
	}
	r.finishOperation(e, "completed", 1)
	for i := 1; i < terminalOperationCapacity; i++ {
		if _, _, err = r.beginOperation(fmt.Sprint(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = r.beginOperation("overflow", nil); err == nil {
		t.Fatal("cache not bounded")
	}
	r.mu.Lock()
	e.created = time.Now().Add(-24 * time.Hour)
	r.mu.Unlock()
	if result, ok := r.operationResult("request"); !ok || result.Status != "completed" {
		t.Fatal("old tombstone lost")
	}
	if _, cached, err = r.beginOperation("request", []byte("a")); err != nil || !cached {
		t.Fatal("old ID not deduplicated at capacity")
	}
	if _, _, err = r.beginOperation("new", nil); err == nil {
		t.Fatal("old records were evicted")
	}

}
func TestTerminalWriteResponseLossDeduplicates(t *testing.T) {
	state, path := terminalTestState(t)
	ring := newTerminalRing()
	peer := &terminalFakePeer{closed: make(chan struct{}), ring: ring}
	runtime := newTerminalRuntime(context.Background(), state, path, peer, ring)
	defer runtime.end(-1)
	lost := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/write" && !lost {
			lost = true
			rec := httptest.NewRecorder()
			runtime.ServeHTTP(rec, r)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		runtime.ServeHTTP(w, r)
	}))
	defer server.Close()
	state.ControlURL = server.URL
	if err := terminalReplacePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	args := []string{"--state", path, "--text", "hello", "--operation-id", "request"}
	if err := terminalControl(context.Background(), "write", args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("lost response reported success")
	}
	var output bytes.Buffer
	if err := terminalControl(context.Background(), "operation", []string{"--state", path, "--operation-id", "request"}, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result terminalOperationResult
	if json.Unmarshal(output.Bytes(), &result) != nil || result.Status != "completed" || result.BytesWritten != 5 {
		t.Fatalf("bad result %s", output.Bytes())
	}
	if err := terminalControl(context.Background(), "write", args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	conflict := append([]string{}, args...)
	conflict[3] = "different"
	if err := terminalControl(context.Background(), "write", conflict, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("conflicting HTTP write accepted")
	}
	if peer.writes.Load() != 1 {
		t.Fatal("SSH input replayed")
	}
}
func TestExecutionTerminationOutcome(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
		known  bool
	}{{nil, "exited", true}, {context.Canceled, "canceled", false}, {context.DeadlineExceeded, "expired", false}, {io.EOF, "connection_lost", false}} {
		reason, known := executionOutcome(tc.err)
		if reason != tc.reason || known != tc.known {
			t.Fatal(reason, known)
		}
	}
}
func TestTerminalFirstTerminationWins(t *testing.T) {
	ring := newTerminalRing()
	ring.finishOutcome(-1, "expired", false)
	ring.finishOutcome(0, "exited", true)
	got := ring.snapshot()
	if got.TerminationReason != "expired" || got.ExitStatusKnown {
		t.Fatal("terminal outcome overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := terminalWait(ctx, []string{"--state", "missing", "--wait", "31s"}, strings.NewReader(""), io.Discard, io.Discard); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("invalid bounds not checked")
	}
}

func TestTerminalWaitReturnsFinalSnapshot(t *testing.T) {
	state, path := terminalTestState(t)
	state.ControlURL = "http://127.0.0.1:1"
	if err := terminalReplacePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	ring := newTerminalRing()
	ring.finishOutcome(0, "exited", true)
	snapshot := ring.snapshot()
	snapshot.SessionID = state.ID
	if err := writeJSONPrivate(strings.TrimSuffix(path, "state.json")+"snapshot.json", snapshot); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := terminalWait(context.Background(), []string{"--state", path, "--wait", "0", "--json"}, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var meta terminalMetadata
	if json.Unmarshal(output.Bytes(), &meta) != nil || !meta.Closed || !meta.ExitStatusKnown || meta.TerminationReason != "exited" {
		t.Fatalf("bad wait result %s", output.Bytes())
	}
}

func TestTerminalCanceledAndExpiredOutcomes(t *testing.T) {
	for _, reason := range []string{"canceled", "expired"} {
		t.Run(reason, func(t *testing.T) {
			state, path := terminalTestState(t)
			var ctx context.Context
			var cancel context.CancelFunc
			if reason == "expired" {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			cancel()
			ring := newTerminalRing()
			peer := &terminalFakePeer{closed: make(chan struct{}), ring: ring}
			runtime := newTerminalRuntime(ctx, state, path, peer, ring)
			runtime.end(-1)
			result := ring.snapshot()
			if result.TerminationReason != reason || result.ExitStatusKnown {
				t.Fatalf("wrong cancellation outcome %#v", result)
			}
		})
	}
}
