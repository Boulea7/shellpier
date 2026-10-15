package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func enhancedTransfer(t *testing.T, direction string, args ...string) (transferSummary, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out bytes.Buffer
	e := transfer(ctx, direction, append([]string{"--json"}, args...), &out, io.Discard)
	var result transferSummary
	if e == nil {
		if e = json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
	}
	return result, e
}

func TestTransferRecursiveRealSFTP(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "中文 source")
	if e := os.MkdirAll(filepath.Join(source, "empty"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(source, "skipdir"), 0700); e != nil {
		t.Fatal(e)
	}
	for name, content := range map[string]string{"data file": "hello 中文", "skip.tmp": "ignore", "skipdir/hidden": "ignore"} {
		if e := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	remote := filepath.Join(dir, "remote tree")
	result, e := enhancedTransfer(t, "upload", "--recursive", "--exclude", "*.tmp", "--exclude", "skipdir", "@"+ticket, source, transferPath(remote))
	if e != nil {
		t.Fatal(e)
	}
	if result.Files != 1 || result.Directories != 2 {
		t.Fatalf("summary: %#v", result)
	}
	local := filepath.Join(dir, "download tree")
	result, e = enhancedTransfer(t, "download", "--recursive", "@"+ticket, transferPath(remote), local)
	if e != nil {
		t.Fatal(e)
	}
	if result.Files != 1 || result.Directories != 2 {
		t.Fatal(result)
	}
	if b, e := os.ReadFile(filepath.Join(local, "data file")); e != nil || string(b) != "hello 中文" {
		t.Fatalf("content %q %v", b, e)
	}
	if i, e := os.Stat(filepath.Join(local, "empty")); e != nil || !i.IsDir() {
		t.Fatal("empty directory missing")
	}
	if _, e = enhancedTransfer(t, "upload", "--recursive", "@"+ticket, source, transferPath(remote)); e == nil {
		t.Fatal("existing root overwritten")
	}
	for _, limits := range [][]string{{"--max-files", "1"}, {"--max-depth", "0"}} {
		target := filepath.Join(dir, "limit-"+limits[0])
		args := append([]string{"--recursive"}, limits...)
		args = append(args, "@"+ticket, source, transferPath(target))
		if _, e = enhancedTransfer(t, "upload", args...); e == nil {
			t.Fatal("capacity accepted")
		}
		if _, e = os.Lstat(target); !os.IsNotExist(e) {
			t.Fatal("planning failure created destination")
		}
	}
	if e = os.Symlink(filepath.Join(source, "data file"), filepath.Join(source, "link")); e == nil {
		if _, e = enhancedTransfer(t, "upload", "--recursive", "@"+ticket, source, transferPath(filepath.Join(dir, "link-tree"))); e == nil {
			t.Fatal("symlink accepted")
		}
	}
}

type transferCancelProgress struct {
	cancel context.CancelFunc
	once   sync.Once
}

func (w *transferCancelProgress) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("transfer_progress")) {
		w.once.Do(w.cancel)
	}
	return len(p), nil
}

func TestTransferResumeRealSFTP(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			dir, ticket := slowTransferEndpoint(t)
			source := filepath.Join(dir, "中文 source file")
			destination := filepath.Join(dir, "final file")
			state := filepath.Join(dir, "resume.json")
			content := bytes.Repeat([]byte("resume 内容\x00"), 32768)
			if e := os.WriteFile(source, content, 0600); e != nil {
				t.Fatal(e)
			}
			from, to := source, transferPath(destination)
			if direction == "download" {
				from, to = transferPath(source), destination
			}
			args := []string{"--resume", "--resume-state", state, "--progress", "jsonl", "@" + ticket, from, to}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			e := transfer(ctx, direction, args, io.Discard, &transferCancelProgress{cancel: cancel})
			if e == nil {
				t.Fatal("cancellation succeeded")
			}
			if _, e = os.Lstat(destination); !os.IsNotExist(e) {
				t.Fatal("interrupted resume exposed final")
			}
			result, e := enhancedTransfer(t, direction, "--resume", "--resume-state", state, "@"+ticket, from, to)
			if e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256(content)
			if result.SHA256 != hex.EncodeToString(sum[:]) || result.Bytes != int64(len(content)) {
				t.Fatal(result)
			}
			got, e := os.ReadFile(destination)
			if e != nil || !bytes.Equal(got, content) {
				t.Fatal("resumed bytes differ")
			}
			if _, e = os.Lstat(state); !os.IsNotExist(e) {
				t.Fatal("completed state retained")
			}
			out, e := runFiles(t, "checksum", "--json", "@"+ticket, transferPath(destination))
			if e != nil {
				t.Fatal(e)
			}
			var checksum struct {
				SHA256 string `json:"sha256"`
				Bytes  int64  `json:"bytes"`
			}
			if e = json.Unmarshal([]byte(out), &checksum); e != nil || checksum.SHA256 != result.SHA256 || checksum.Bytes != result.Bytes {
				t.Fatalf("checksum %s %v", out, e)
			}
		})
	}
}

func TestTransferResumeRefusesChangesAndUnknownFiles(t *testing.T) {
	for _, change := range []string{"source", "partial", "final"} {
		t.Run(change, func(t *testing.T) {
			dir, ticket := slowTransferEndpoint(t)
			source := filepath.Join(dir, "source")
			dest := filepath.Join(dir, "dest")
			state := filepath.Join(dir, "resume.json")
			content := bytes.Repeat([]byte("safe-data"), 32768)
			if e := os.WriteFile(source, content, 0600); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = transfer(ctx, "upload", []string{"--resume", "--resume-state", state, "--progress", "jsonl", "@" + ticket, source, transferPath(dest)}, io.Discard, &transferCancelProgress{cancel: cancel})
			data, e := os.ReadFile(state)
			if e != nil {
				t.Fatal(e)
			}
			var m transferManifest
			if e = json.Unmarshal(data, &m); e != nil {
				t.Fatal(e)
			}
			part := m.Partial
			if runtime.GOOS == "windows" {
				part = strings.TrimPrefix(part, "/")
				part = filepath.FromSlash(part)
			}
			changed := source
			if change == "partial" {
				changed = part
			}
			if change == "final" {
				changed = dest
			}
			if e = os.WriteFile(changed, []byte("foreign content"), 0600); e != nil {
				t.Fatal(e)
			}
			if _, e = enhancedTransfer(t, "upload", "--resume", "--resume-state", state, "@"+ticket, source, transferPath(dest)); e == nil {
				t.Fatal("changed resource accepted")
			}
			if b, e := os.ReadFile(changed); e != nil || string(b) != "foreign content" {
				t.Fatal("foreign file changed or deleted")
			}
			if _, e = os.Lstat(state); e != nil {
				t.Fatal("state deleted after failure")
			}
		})
	}
}

func TestTransferPlanPortableNames(t *testing.T) {
	for _, name := range []string{"CON", "nul.txt", "COM1.log", "trailing.", "trailing ", "a:b", "../escape", "a\\b"} {
		if safeTransferName(name) {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "A"), nil, 0600)
	_ = os.WriteFile(filepath.Join(dir, "a"), nil, 0600)
	entries, _ := os.ReadDir(dir)
	if len(entries) == 2 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, e := planTransferTree(ctx, transferFS{}, dir, nil, 10, 2); e == nil {
			t.Fatal("case collision accepted")
		}
	}
}

func TestTransferEnhancedOptionValidation(t *testing.T) {
	for _, options := range [][]string{{"--recursive", "--resume"}, {"--resume-state", "x"}, {"--exclude", "**"}, {"--progress", "plain"}, {"--max-files", "0"}, {"--max-depth", "129"}} {
		if _, e := enhancedTransfer(t, "upload", append(options, "missing", "a", "b")...); e == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
