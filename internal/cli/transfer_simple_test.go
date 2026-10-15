package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type transferNoReadProgress struct {
	target string
	err    error
}

func (w *transferNoReadProgress) Write(p []byte) (int, error) {
	w.err = os.Chmod(w.target, 0200)
	if w.err != nil {
		return 0, w.err
	}
	return len(p), nil
}

func TestOrdinaryTransferDoesNotRereadTarget(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix read permission enforcement")
	}
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			dir, ticket := slowTransferEndpoint(t)
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if e := os.WriteFile(source, []byte("direct-copy"), 0600); e != nil {
				t.Fatal(e)
			}
			from, to := source, transferPath(target)
			if direction == "download" {
				from, to = transferPath(source), target
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var out bytes.Buffer
			progress := &transferNoReadProgress{target: target}
			e := transfer(ctx, direction, []string{"--json", "--progress", "jsonl", "@" + ticket, from, to}, &out, progress)
			defer os.Chmod(target, 0600)
			if e != nil {
				t.Fatalf("ordinary transfer tried to reread its write-only destination: %v", e)
			}
			var summary map[string]any
			if e = json.Unmarshal(out.Bytes(), &summary); e != nil {
				t.Fatal(e)
			}
			for _, removed := range []string{"sha256", "verified", "verified_files"} {
				if _, ok := summary[removed]; ok {
					t.Fatalf("removed validation output %q remains", removed)
				}
			}
			if e = os.Chmod(target, 0600); e != nil {
				t.Fatal(e)
			}
			if data, e := os.ReadFile(target); e != nil || string(data) != "direct-copy" {
				t.Fatal("copied content differs")
			}
		})
	}
}

func TestSkipExistingNeverReadsDifferentFileContents(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if e := os.WriteFile(source, []byte("new source"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(target, []byte("keep this different file"), 0600); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		for _, p := range []string{source, target} {
			if e := os.Chmod(p, 0000); e != nil {
				t.Fatal(e)
			}
			defer os.Chmod(p, 0600)
		}
	}
	for _, direction := range []string{"upload", "download"} {
		from, to := source, transferPath(target)
		if direction == "download" {
			from, to = transferPath(source), target
		}
		result, e := enhancedTransfer(t, direction, "--skip-existing", "@"+ticket, from, to)
		if e != nil || result.SkippedFiles != 1 || result.Bytes != 0 {
			t.Fatalf("skip existing %#v %v", result, e)
		}
	}
	if e := os.Chmod(target, 0600); e != nil {
		t.Fatal(e)
	}
	if data, e := os.ReadFile(target); e != nil || string(data) != "keep this different file" {
		t.Fatal("existing contents changed")
	}
}

func TestRemovedTransferChecksAreUnavailable(t *testing.T) {
	for _, flag := range []string{"--verify", "--skip-identical"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e := transfer(ctx, "upload", []string{flag, "missing", "source", "target"}, io.Discard, io.Discard)
		cancel()
		if e == nil {
			t.Fatalf("removed flag %s accepted", flag)
		}
	}
	if _, e := runFiles(t, "checksum", "missing", "target"); e == nil {
		t.Fatal("removed checksum subcommand accepted")
	}
}
