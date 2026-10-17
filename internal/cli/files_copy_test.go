package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Boulea7/shellpier/internal/access"
	"github.com/pkg/sftp"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesCopyRealSFTP(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	for _, data := range [][]byte{{}, bytes.Repeat([]byte{0, 255, 1, 128}, 100000)} {
		name := "large"
		if len(data) == 0 {
			name = "empty"
		}
		source, dest := filepath.Join(dir, name), filepath.Join(dir, name+"-copy")
		if err := os.WriteFile(source, data, 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"copy", "--json", "@" + ticket, transferPath(source), transferPath(dest)}
		out, err := runFiles(t, append(args, "--dry-run")...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatal("dry-run wrote")
		}
		out, err = runFiles(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Bytes     int64
			Transport string
			DryRun    bool `json:"dry_run"`
		}
		if err = json.Unmarshal([]byte(out), &result); err != nil || result.Bytes != int64(len(data)) || result.Transport != "client-stream" || result.DryRun {
			t.Fatalf("result %s %v", out, err)
		}
		copied, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(copied, data) {
			t.Fatalf("content %v", err)
		}
		if _, err = runFiles(t, args...); err == nil {
			t.Fatal("existing accepted")
		}
		if err = os.WriteFile(dest, []byte("independent"), 0600); err != nil {
			t.Fatal(err)
		}
		unchanged, err := os.ReadFile(source)
		if err != nil || !bytes.Equal(unchanged, data) {
			t.Fatal("source changed")
		}
	}
	source := filepath.Join(dir, "large")
	for _, p := range []string{dir, filepath.Join(dir, "missing")} {
		if _, err := runFiles(t, "copy", "@"+ticket, transferPath(p), transferPath(filepath.Join(dir, "bad"))); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if _, err := runFiles(t, "copy", "@"+ticket, transferPath(source), transferPath(filepath.Join(dir, "missing", "dest"))); err == nil {
		t.Fatal("missing parent accepted")
	}
	link := filepath.Join(dir, "link")
	if os.Symlink(source, link) == nil {
		if _, err := runFiles(t, "copy", "@"+ticket, transferPath(link), transferPath(filepath.Join(dir, "link-copy"))); err == nil {
			t.Fatal("source symlink accepted")
		}
		if _, err := runFiles(t, "copy", "@"+ticket, transferPath(source), transferPath(link)); err == nil {
			t.Fatal("destination symlink accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := files(ctx, []string{"copy", "@" + ticket, transferPath(source), transferPath(filepath.Join(dir, "canceled"))}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestFilesCopyRootAndReadonly(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, mode := range []string{"sftp", "sftp-readonly"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "source"), []byte("rooted"), 0600); err != nil {
				t.Fatal(err)
			}
			ticket := filepath.Join(t.TempDir(), "access.json")
			state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--root", root, "--access-mode", mode, "--output", ticket, "--json")
			defer func() { invoke(t, "stop", "--state", state); awaitHost(t, done, state) }()
			_, err := runFiles(t, "copy", "@"+ticket, "/source", "/dest")
			if (err == nil) != (mode == "sftp") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			outside := t.TempDir()
			if os.Symlink(outside, filepath.Join(root, "escape")) == nil {
				if _, err := runFiles(t, "copy", "@"+ticket, "/source", "/escape/dest"); err == nil {
					t.Fatal("root escape accepted")
				}
			}
		})
	}
}

func TestFilesCopyCanceledAfterConnection(t *testing.T) {
	dir, ticketPath := slowTransferEndpoint(t)
	source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "partial")
	if err := os.WriteFile(source, bytes.Repeat([]byte("x"), 128*1024), 0600); err != nil {
		t.Fatal(err)
	}
	ticket, err := access.Read("@" + ticketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	conn, err := access.DialRetry(ctx, ticket, "", access.RetryOptions{MinDelay: time.Millisecond, MaxDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sf, err := sftp.NewClient(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err = filesCopy(canceled, sf, transferPath(source), transferPath(dest), false, false, io.Discard)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "incomplete destination may remain") {
		t.Fatalf("error %v", err)
	}
	if _, err = sf.Stat(transferPath(source)); err != nil {
		t.Fatalf("connection unusable after cancellation: %v", err)
	}
	if _, err = os.Stat(dest); err != nil {
		t.Fatalf("partial should remain: %v", err)
	}
	if err = sf.Remove(transferPath(dest)); err != nil {
		t.Fatal(err)
	}
}
