package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransferStreamBinaryRoundtrip(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	for _, size := range []int{0, 256, 2*1024*1024 + 17} {
		t.Run(string(rune('a'+size%26)), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i)
			}
			remote := filepath.Join(dir, "stream-"+t.Name()[strings.LastIndex(t.Name(), "/")+1:])
			var summary, progress bytes.Buffer
			err := transferWithInputAndProgress(context.Background(), "upload", []string{"--json", "--progress", "jsonl", "@" + ticket, "-", transferPath(remote)}, bytes.NewReader(data), &summary, io.Discard, &progress)
			if err != nil {
				t.Fatal(err)
			}
			var result transferSummary
			if err := json.Unmarshal(summary.Bytes(), &result); err != nil || result.Bytes != int64(size) {
				t.Fatalf("summary %s %v", summary.Bytes(), err)
			}
			var output bytes.Buffer
			if err := transferWithInput(context.Background(), "download", []string{"@" + ticket, transferPath(remote), "-"}, nil, &output, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), data) {
				t.Fatal("raw output differs or contains summary")
			}
			if size > 0 && !bytes.Contains(progress.Bytes(), []byte("transfer_progress")) {
				t.Fatal("missing separate progress")
			}
			if err := transferWithInput(context.Background(), "upload", []string{"@" + ticket, "-", transferPath(remote)}, strings.NewReader("foreign"), io.Discard, io.Discard); err == nil {
				t.Fatal("overwrote existing file")
			}
			got, err := os.ReadFile(remote)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("existing contents changed")
			}
		})
	}
}

func TestTransferStreamValidation(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		for _, option := range []string{"--recursive", "--resume", "--preserve-times", "--dry-run", "--skip-existing"} {
			args := []string{option, "missing-ticket", "-", "remote"}
			if direction == "download" {
				args = []string{option, "missing-ticket", "remote", "-"}
			}
			err := transferWithInput(context.Background(), direction, args, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "stream transfers") {
				t.Fatalf("%s %s: %v", direction, option, err)
			}
		}
	}
	if err := transfer(context.Background(), "upload", []string{"missing", "-", "remote"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "standard input") {
		t.Fatal(err)
	}
	if err := transfer(context.Background(), "download", []string{"--json", "missing", "remote", "-"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "raw bytes") {
		t.Fatal(err)
	}
}

type streamFailReader struct{ calls int }

func (r *streamFailReader) Read(p []byte) (int, error) {
	r.calls++
	return copy(p, []byte("partial\x00")), io.ErrUnexpectedEOF
}

type streamFailWriter struct{}

func (streamFailWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTransferStreamFailuresNeverReplay(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	input := &streamFailReader{}
	err := transferWithInput(context.Background(), "upload", []string{"@" + ticket, "-", transferPath(filepath.Join(dir, "partial"))}, input, io.Discard, io.Discard)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "incomplete destination may remain") || input.calls != 1 {
		t.Fatalf("%v calls=%d", err, input.calls)
	}
	if err := os.WriteFile(filepath.Join(dir, "source"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	err = transferWithInput(context.Background(), "download", []string{"@" + ticket, transferPath(filepath.Join(dir, "source")), "-"}, nil, streamFailWriter{}, io.Discard)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestTransferStreamCancelBlockedInput(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	remote := filepath.Join(dir, "blocked")
	go func() {
		done <- transferWithInput(ctx, "upload", []string{"@" + ticket, "-", transferPath(remote)}, reader, io.Discard, io.Discard)
	}()
	waitTransferPath(t, remote)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked input ignored cancellation")
	}
}

func TestTransferStreamRootAndReadonly(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, readonly := range []bool{false, true} {
		mode := "sftp"
		if readonly {
			mode = "sftp-readonly"
		}
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "shared")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(base, "outside")
			if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "source"), []byte("root\x00bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			ticket := filepath.Join(t.TempDir(), "access.json")
			state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--root", root, "--access-mode", mode, "--output", ticket, "--json")
			defer func() { invoke(t, "stop", "--state", state); awaitHost(t, done, state) }()
			var out bytes.Buffer
			if err := transferWithInput(context.Background(), "download", []string{"@" + ticket, "/source", "-"}, nil, &out, io.Discard); err != nil || out.String() != "root\x00bytes" {
				t.Fatalf("root download %q %v", out.String(), err)
			}
			err := transferWithInput(context.Background(), "upload", []string{"@" + ticket, "-", "/created"}, strings.NewReader("created"), io.Discard, io.Discard)
			if readonly && err == nil || !readonly && err != nil {
				t.Fatalf("upload readonly=%t: %v", readonly, err)
			}
			if readonly {
				if _, err := os.Stat(filepath.Join(root, "created")); !os.IsNotExist(err) {
					t.Fatal("readonly wrote destination")
				}
			}
			for _, name := range []string{"../outside", "/../outside", transferPath(outside)} {
				var escaped bytes.Buffer
				if err := transferWithInput(context.Background(), "download", []string{"@" + ticket, name, "-"}, nil, &escaped, io.Discard); err == nil {
					t.Fatalf("read outside with %s", name)
				}
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "private" {
				t.Fatal("outside file changed")
			}
		})
	}
}

type streamBoundedReader struct {
	remaining int64
	maxRead   int
}

func (r *streamBoundedReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	clear(p[:n])
	r.remaining -= int64(n)
	return n, nil
}

func TestTransferStreamBoundedInput(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	input := &streamBoundedReader{remaining: 4*1024*1024 + 1}
	err := transferWithInput(context.Background(), "upload", []string{"@" + ticket, "-", transferPath(filepath.Join(dir, "bounded"))}, input, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if input.maxRead > 64*1024 || input.remaining != 0 {
		t.Fatalf("max read=%d remaining=%d", input.maxRead, input.remaining)
	}
}

func TestTransferStreamDisconnectBlockedInput(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	ticket := filepath.Join(dir, "access.json")
	state, hostDone := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--work-dir", dir, "--output", ticket, "--json")
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	remote := filepath.Join(dir, "disconnect")
	go func() {
		done <- transferWithInput(ctx, "upload", []string{"@" + ticket, "-", transferPath(remote)}, reader, io.Discard, io.Discard)
	}()
	waitTransferPath(t, remote)
	invoke(t, "stop", "--state", state)
	awaitHost(t, hostDone, state)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "incomplete destination may remain") {
			t.Fatalf("disconnect result: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected upload kept waiting for input")
	}
}

type streamBlockedOutput struct {
	*io.PipeWriter
	started chan struct{}
	once    sync.Once
}

func (w *streamBlockedOutput) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.PipeWriter.Write(p)
}

func TestTransferStreamCancelBlockedOutput(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, disconnect := range []bool{false, true} {
		name := "cancel"
		if disconnect {
			name = "disconnect"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source")
			if err := os.WriteFile(source, []byte("binary\x00payload"), 0600); err != nil {
				t.Fatal(err)
			}
			ticket := filepath.Join(dir, "access.json")
			state, hostDone := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--work-dir", dir, "--output", ticket, "--json")
			hostStopped := false
			defer func() {
				if !hostStopped {
					invoke(t, "stop", "--state", state)
					awaitHost(t, hostDone, state)
				}
			}()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			output := &streamBlockedOutput{PipeWriter: writer, started: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- transferWithInput(ctx, "download", []string{"@" + ticket, transferPath(source), "-"}, nil, output, io.Discard)
			}()
			select {
			case <-output.started:
			case <-time.After(3 * time.Second):
				t.Fatal("download never reached output Write")
			}
			if disconnect {
				invoke(t, "stop", "--state", state)
				awaitHost(t, hostDone, state)
				hostStopped = true
			} else {
				cancel()
			}
			// Neither pipe end is closed by this test before completion.
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("blocked download reported success")
				}
				if !disconnect && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("blocked output ignored cancellation/disconnect")
			}
		})
	}
}
