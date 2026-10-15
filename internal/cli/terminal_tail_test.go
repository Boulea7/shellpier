package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func terminalTailFixture(t *testing.T, ring *terminalRing) (string, *atomic.Int32) {
	t.Helper()
	state, path := terminalTestState(t)
	reads := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !terminalAuthorized(req, state.Secret) || req.URL.Path != "/read" {
			t.Error("tail attempted unauthorized or mutating operation")
			http.Error(w, "invalid", 403)
			return
		}
		var body struct {
			Cursor uint64 `json:"cursor"`
			Limit  int    `json:"limit"`
			WaitMS int64  `json:"wait_ms"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		reads.Add(1)
		result, err := ring.read(req.Context(), body.Cursor, body.Limit, time.Duration(body.WaitMS)*time.Millisecond)
		if err != nil {
			http.Error(w, "read cancelled", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(server.Close)
	state.ControlURL = server.URL
	if err := terminalReplacePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	return path, reads
}

func TestTerminalTailBinaryCursorSnapshot(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "snapshot"}[snapshot], func(t *testing.T) {
			ring := newTerminalRing()
			payload := []byte{0, 255, 128, '\r', '\n', 2}
			_, _ = ring.Write(payload)
			ring.finish(7)
			path, reads := terminalTailFixture(t, ring)
			if snapshot {
				state, err := terminalReadState(path, false)
				if err != nil {
					t.Fatal(err)
				}
				data := ring.snapshot()
				data.SessionID = state.ID
				if err := writeJSONPrivate(filepath.Join(filepath.Dir(path), "snapshot.json"), data); err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostic bytes.Buffer
			err := terminalTail(context.Background(), []string{"--state", path, "--cursor", "2"}, &out, &diagnostic)
			if err != nil || !bytes.Equal(out.Bytes(), payload[2:]) || diagnostic.Len() != 0 {
				t.Fatalf("tail %v %q %q", err, out.Bytes(), diagnostic.String())
			}
			if snapshot && reads.Load() != 0 {
				t.Fatal("snapshot read contacted worker")
			}
		})
	}
}

func TestTerminalTailFollowDrainsClosedChunks(t *testing.T) {
	ring := newTerminalRing()
	payload := bytes.Repeat([]byte{0, 255, 'x'}, terminalDefaultRead)
	_, _ = ring.Write(payload)
	ring.finish(9)
	path, reads := terminalTailFixture(t, ring)
	var out bytes.Buffer
	if err := terminalTail(context.Background(), []string{"--state", path, "--follow"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), payload) || reads.Load() < 3 {
		t.Fatalf("closed output truncated: %d bytes, %d reads", out.Len(), reads.Load())
	}
}

func TestTerminalTailSingleReadAndDropped(t *testing.T) {
	ring := newTerminalRing()
	payload := bytes.Repeat([]byte("x"), terminalCapacity+19)
	_, _ = ring.Write(payload)
	path, reads := terminalTailFixture(t, ring)
	var out, diagnostic bytes.Buffer
	if err := terminalTail(context.Background(), []string{"--state", path}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if out.Len() != terminalCapacity || reads.Load() != 1 || !strings.Contains(diagnostic.String(), "dropped 19 output bytes") {
		t.Fatalf("tail len=%d reads=%d stderr=%q", out.Len(), reads.Load(), diagnostic.String())
	}
	if _, err := ring.Write([]byte("still alive")); err != nil {
		t.Fatal("tail closed terminal")
	}
}

func TestTerminalTailLiveFollowAndCancellation(t *testing.T) {
	ring := newTerminalRing()
	path, reads := terminalTailFixture(t, ring)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output, writer := io.Pipe()
	defer output.Close()
	done := make(chan error, 1)
	go func() { done <- terminalTail(ctx, []string{"--state", path, "--follow"}, writer, io.Discard) }()
	_, _ = ring.Write([]byte("live"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(output, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "live" {
		t.Fatal(string(buf))
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tail cancellation stuck")
	}
	if reads.Load() == 0 {
		t.Fatal("no reads")
	}
	if _, err := ring.Write([]byte("after tail")); err != nil {
		t.Fatal("tail cancellation closed terminal")
	}
}

func TestTerminalTailBlockedOutputTimeout(t *testing.T) {
	ring := newTerminalRing()
	_, _ = ring.Write([]byte("unconsumed"))
	path, _ := terminalTailFixture(t, ring)
	output, writer := io.Pipe()
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		done <- terminalTail(context.Background(), []string{"--state", path, "--timeout", "100ms"}, writer, io.Discard)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked stdout did not cancel")
	}
	if _, err := ring.Write([]byte("still alive")); err != nil {
		t.Fatal("timeout closed terminal")
	}
}

func TestTerminalTailEarlyUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"--timeout", "0s"}, {"--timeout", "25h"}, {"--cursor", "-1"}, {"extra"}} {
		var out bytes.Buffer
		err := terminalTail(context.Background(), append([]string{"--state", "missing-state"}, args...), &out, io.Discard)
		var usage *UsageError
		if err == nil || !errors.As(err, &usage) || out.Len() != 0 || strings.Contains(err.Error(), "read file") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

type terminalTailFailedWriter struct{}

func (terminalTailFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTerminalTailOutputFailureDoesNotCloseTerminal(t *testing.T) {
	ring := newTerminalRing()
	_, _ = ring.Write([]byte("output"))
	path, _ := terminalTailFixture(t, ring)
	err := terminalTail(context.Background(), []string{"--state", path, "--follow"}, terminalTailFailedWriter{}, io.Discard)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output error: %v", err)
	}
	if _, err := ring.Write([]byte("still alive")); err != nil {
		t.Fatal("output failure closed terminal")
	}
}

func TestTerminalTailDispatchAndGlobalJSON(t *testing.T) {
	ring := newTerminalRing()
	_, _ = ring.Write([]byte("raw"))
	path, _ := terminalTailFixture(t, ring)
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"terminal", "tail", "--state", path}, nil, &out, io.Discard, "test"); err != nil || out.String() != "raw" {
		t.Fatalf("dispatch: %v %q", err, out.String())
	}
	out.Reset()
	err := Run(context.Background(), []string{"--json", "terminal", "tail", "--state", "missing-state"}, nil, &out, io.Discard, "test")
	var reported *ReportedError
	if err == nil || !errors.As(err, &reported) || out.Len() != 0 {
		t.Fatalf("global JSON refusal: %v %q", err, out.String())
	}
}

type terminalTailReplacingWriter struct {
	bytes.Buffer
	replace func() error
}

func (w *terminalTailReplacingWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	if err == nil && w.replace != nil {
		replace := w.replace
		w.replace = nil
		err = replace()
	}
	return n, err
}

func TestTerminalTailRejectsReplacedSessionBetweenChunks(t *testing.T) {
	for _, changed := range []string{"session", "secret", "endpoint"} {
		t.Run(changed, func(t *testing.T) {
			ring := newTerminalRing()
			_, _ = ring.Write(bytes.Repeat([]byte("A"), terminalDefaultRead*2))
			path, _ := terminalTailFixture(t, ring)
			initial, err := terminalReadState(path, false)
			if err != nil {
				t.Fatal(err)
			}
			output := &terminalTailReplacingWriter{}
			output.replace = func() error {
				replacement := initial
				switch changed {
				case "session":
					replacement.ID, _ = secret()
					replacement.Secret, _ = secret()
					replacement.ControlURL = "http://127.0.0.1:1"
				case "secret":
					replacement.Secret, _ = secret()
				case "endpoint":
					replacement.ControlURL = "http://127.0.0.1:1"
				}
				if err := terminalReplacePrivate(path, replacement); err != nil {
					return err
				}
				other := newTerminalRing()
				_, _ = other.Write(bytes.Repeat([]byte("B"), terminalDefaultRead*2))
				other.finish(0)
				snapshot := other.snapshot()
				snapshot.SessionID = replacement.ID
				return writeJSONPrivate(filepath.Join(filepath.Dir(path), "snapshot.json"), snapshot)
			}
			err = terminalTail(context.Background(), []string{"--state", path, "--follow"}, output, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "session identity changed") {
				t.Fatalf("changed %s identity accepted: %v", changed, err)
			}
			if !bytes.Equal(output.Bytes(), bytes.Repeat([]byte("A"), terminalDefaultRead)) {
				t.Fatalf("mixed replaced terminal bytes: length=%d", output.Len())
			}
		})
	}
}
