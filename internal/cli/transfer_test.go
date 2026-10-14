package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func transferPath(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		return "/" + path
	}
	return path
}
func runTransfer(t *testing.T, command string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	e := transfer(context.Background(), command, args, &out, io.Discard)
	return out.String(), e
}
func TestTransferDirectSingleFiles(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	ticketFile := filepath.Join(dir, "access.json")
	state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--output", ticketFile, "--json", "--work-dir", dir)
	for _, content := range []string{"Unicode 中文 and spaces\n", ""} {
		name := "中文 source file"
		if content == "" {
			name = "empty source"
		}
		source := filepath.Join(dir, name)
		remote := filepath.Join(dir, name+" remote")
		local := filepath.Join(dir, name+" downloaded")
		if e := os.WriteFile(source, []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
		out, e := runTransfer(t, "upload", "--json", "@"+ticketFile, source, transferPath(remote))
		if e != nil {
			t.Fatalf("upload failed: %v", e)
		}
		var result struct {
			Bytes int64 `json:"bytes"`
		}
		if json.Unmarshal([]byte(out), &result) != nil || result.Bytes != int64(len(content)) {
			t.Fatal("upload byte count mismatch")
		}
		if _, e = runTransfer(t, "download", "@"+ticketFile, transferPath(remote), local, "--json"); e != nil {
			t.Fatalf("download failed: %v", e)
		}
		data, e := os.ReadFile(local)
		if e != nil || string(data) != content {
			t.Fatal("roundtrip content mismatch")
		}
		if _, e = runTransfer(t, "upload", "@"+ticketFile, source, transferPath(remote)); e == nil {
			t.Fatal("upload overwrote destination")
		}
		if _, e = runTransfer(t, "download", "@"+ticketFile, transferPath(remote), local); e == nil {
			t.Fatal("download overwrote destination")
		}
		data, e = os.ReadFile(local)
		if e != nil || string(data) != content {
			t.Fatal("existing destination changed")
		}
	}
	if _, e := runTransfer(t, "upload", "@"+ticketFile, dir, transferPath(filepath.Join(dir, "bad"))); e == nil {
		t.Fatal("directory upload accepted")
	}
	if _, e := runTransfer(t, "download", "@"+ticketFile, transferPath(dir), filepath.Join(dir, "bad")); e == nil {
		t.Fatal("directory download accepted")
	}
	invoke(t, "stop", "--state", state)
	awaitHost(t, done, state)
}

// Slow reads make cancellation deterministic without allocating a large payload.
type transferSlowConn struct{ net.Conn }

func (c transferSlowConn) Read(p []byte) (int, error) {
	if len(p) > 4096 {
		p = p[:4096]
	}
	time.Sleep(time.Millisecond)
	return c.Conn.Read(p)
}
func slowTransferEndpoint(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	a, h, e := access.NewPair(time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	host, e := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if e != nil {
		t.Fatal(e)
	}
	pub, _, _, _, e := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	a.Addresses = []string{ln.Addr().String()}
	raw, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	ticket := filepath.Join(dir, "access.json")
	if e = os.WriteFile(ticket, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			go (&sshd.Server{Signer: host, AuthorizedKey: pub, WorkDir: dir}).ServeConn(ctx, transferSlowConn{conn})
		}
	}()
	t.Cleanup(func() { cancel(); ln.Close(); <-done })
	return dir, ticket
}
func waitTransferPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, e := os.Stat(path); e == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer destination was not created")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestUploadCancellationPreservesPartialDestination(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "partial")
	f, e := os.Create(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(4 << 20); e != nil {
		t.Fatal(e)
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- transfer(ctx, "upload", []string{"@" + ticket, source, transferPath(destination)}, io.Discard, io.Discard)
	}()
	waitTransferPath(t, destination)
	cancel()
	select {
	case e := <-done:
		if e == nil || !strings.Contains(e.Error(), "incomplete destination may remain:") {
			t.Fatal("upload cancellation did not disclose partial destination")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled upload did not stop")
	}
	if _, e = os.Stat(destination); e != nil {
		t.Fatal("remote incomplete destination was removed")
	}
}
func TestTransferRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"--timeout", "0", "missing", "a", "b"}, {"missing", "a"}, {"missing", "a", "b", "extra"}, {"missing", "a", "\x00"}} {
		if _, e := runTransfer(t, "upload", args...); e == nil {
			t.Fatal("invalid transfer arguments accepted")
		}
	}
}

func (c transferSlowConn) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		chunk := len(p)
		if chunk > 4096 {
			chunk = 4096
		}
		time.Sleep(time.Millisecond)
		n, e := c.Conn.Write(p[:chunk])
		total += n
		p = p[n:]
		if e != nil {
			return total, e
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func TestDownloadCancellationCleansOwnedFile(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "partial")
	f, e := os.Create(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(4 << 20); e != nil {
		t.Fatal(e)
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- transfer(ctx, "download", []string{"@" + ticket, transferPath(source), destination}, io.Discard, io.Discard)
	}()
	waitTransferPath(t, destination)
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancelled download succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled download did not stop")
	}
	if _, e = os.Stat(destination); !os.IsNotExist(e) {
		t.Fatal("cancelled download left its incomplete local file")
	}
}
func TestTransferCancellationPreservesReplacedPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replacing an open path requires Unix unlink semantics")
	}
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			dir, ticket := slowTransferEndpoint(t)
			source := filepath.Join(dir, "source")
			destination := filepath.Join(dir, "destination")
			f, e := os.Create(source)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.Truncate(4 << 20); e != nil {
				t.Fatal(e)
			}
			f.Close()
			from, to := source, transferPath(destination)
			if direction == "download" {
				from, to = transferPath(source), destination
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- transfer(ctx, direction, []string{"@" + ticket, from, to}, io.Discard, io.Discard) }()
			waitTransferPath(t, destination)
			replacement := filepath.Join(dir, "replacement")
			if e = os.WriteFile(replacement, []byte("replacement must survive"), 0600); e != nil {
				t.Fatal(e)
			}
			if e = os.Rename(replacement, destination); e != nil {
				t.Fatal(e)
			}
			cancel()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("cancelled transfer succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled transfer did not stop")
			}
			b, e := os.ReadFile(destination)
			if e != nil || string(b) != "replacement must survive" {
				t.Fatal("transfer removed or modified replaced destination")
			}
		})
	}
}

func TestTransferHelp(t *testing.T) {
	for _, command := range []string{"upload", "download"} {
		for _, help := range []string{"-h", "--help"} {
			var out bytes.Buffer
			e := transfer(context.Background(), command, []string{help}, io.Discard, &out)
			if !errors.Is(e, flag.ErrHelp) || !strings.Contains(out.String(), "Usage of "+command) {
				t.Fatalf("%s %s did not show flag help", command, help)
			}
		}
	}
}
