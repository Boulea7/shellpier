package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferDryRunReadOnlyRealSFTP(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	if e := os.MkdirAll(filepath.Join(source, "empty"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(source, "data"), []byte("preview"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, direction := range []string{"upload", "download"} {
		from, to := source, transferPath(filepath.Join(dir, direction))
		if direction == "download" {
			from, to = transferPath(source), filepath.Join(dir, direction)
		}
		result, e := enhancedTransfer(t, direction, "--dry-run", "--recursive", "--verify", "--preserve-times", "@"+ticket, from, to)
		if e != nil {
			t.Fatal(e)
		}
		if !result.DryRun || len(result.Plan) != 3 || result.PlannedBytes != 7 || result.Bytes != 0 || result.Files != 0 || result.Directories != 0 {
			t.Fatalf("dry plan: %#v", result)
		}
		if _, e = os.Lstat(filepath.Join(dir, direction)); !os.IsNotExist(e) {
			t.Fatal("dry-run created destination")
		}
	}
	file := filepath.Join(source, "data")
	target := filepath.Join(dir, "single")
	result, e := enhancedTransfer(t, "upload", "--dry-run", "@"+ticket, file, transferPath(target))
	if e != nil || len(result.Plan) != 1 || result.PlannedBytes != 7 {
		t.Fatalf("single plan %#v %v", result, e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() != "source" && entry.Name() != "access.json" {
			t.Fatalf("dry-run created state %q", entry.Name())
		}
	}
}

func TestTransferVerifyAndPreserveTimesRealSFTP(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	sub := filepath.Join(source, "sub")
	if e := os.MkdirAll(sub, 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(sub, "中文 file")
	content := []byte("verified bytes")
	if e := os.WriteFile(file, content, 0600); e != nil {
		t.Fatal(e)
	}
	modified := time.Unix(1577836800, 0)
	for _, p := range []string{file, sub, source} {
		if e := os.Chtimes(p, modified, modified); e != nil {
			t.Fatal(e)
		}
	}
	remote := filepath.Join(dir, "remote")
	result, e := enhancedTransfer(t, "upload", "--recursive", "--verify", "--preserve-times", "@"+ticket, source, transferPath(remote))
	if e != nil {
		t.Fatal(e)
	}
	if !result.Verified || result.VerifiedFiles != 1 || result.TimesPreserved != 3 || result.MtimePrecision != "seconds" {
		t.Fatalf("summary %#v", result)
	}
	for _, p := range []string{remote, filepath.Join(remote, "sub"), filepath.Join(remote, "sub", "中文 file")} {
		info, e := os.Stat(p)
		if e != nil || !info.ModTime().Equal(modified) {
			t.Fatalf("mtime %s: %v %v", p, info, e)
		}
	}
	destination := filepath.Join(dir, "download")
	result, e = enhancedTransfer(t, "download", "--verify", "--preserve-times", "@"+ticket, transferPath(filepath.Join(remote, "sub", "中文 file")), destination)
	if e != nil || !result.Verified || result.VerifiedFiles != 1 || result.TimesPreserved != 1 {
		t.Fatalf("single summary %#v %v", result, e)
	}
	info, e := os.Stat(destination)
	if e != nil || !info.ModTime().Equal(modified) {
		t.Fatal("download mtime lost")
	}
}

func TestTransferSkipIdenticalPreflightsConflicts(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "dest")
	for _, p := range []string{source, dest} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, name := range []string{"a-new", "z-conflict"} {
		if e := os.WriteFile(filepath.Join(source, name), []byte("source"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	conflict := filepath.Join(dest, "z-conflict")
	if e := os.WriteFile(conflict, []byte("foreign"), 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"--recursive", "--skip-identical", "--verify", "--preserve-times", "@" + ticket, source, transferPath(dest)}
	if _, e := enhancedTransfer(t, "upload", args...); e == nil {
		t.Fatal("different destination accepted")
	}
	if _, e := os.Lstat(filepath.Join(dest, "a-new")); !os.IsNotExist(e) {
		t.Fatal("wrote new file before conflict preflight")
	}
	if e := os.WriteFile(conflict, []byte("source"), 0600); e != nil {
		t.Fatal(e)
	}
	unchanged := time.Unix(1577836800, 0)
	if e := os.Chtimes(conflict, unchanged, unchanged); e != nil {
		t.Fatal(e)
	}
	result, e := enhancedTransfer(t, "upload", args...)
	if e != nil {
		t.Fatal(e)
	}
	if result.Files != 1 || result.SkippedFiles != 1 || result.Directories != 0 || result.VerifiedFiles != 2 || result.TimesPreserved != 1 {
		t.Fatalf("summary %#v", result)
	}
	info, e := os.Stat(conflict)
	if e != nil || !info.ModTime().Equal(unchanged) {
		t.Fatal("skipped file metadata changed")
	}
	result, e = enhancedTransfer(t, "upload", args...)
	if e != nil || result.Files != 0 || result.SkippedFiles != 2 || result.TimesPreserved != 0 {
		t.Fatalf("repeated tree %#v %v", result, e)
	}
	result, e = enhancedTransfer(t, "upload", "--skip-identical", "--verify", "--preserve-times", "@"+ticket, filepath.Join(source, "z-conflict"), transferPath(conflict))
	if e != nil || result.SkippedFiles != 1 || !result.Verified || result.TimesPreserved != 0 {
		t.Fatalf("single skip %#v %v", result, e)
	}
}

func TestTransferVerifyRejectsSourceAndTargetChanges(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	for _, p := range []string{source, target} {
		if e := os.WriteFile(p, []byte("content"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	info, digest, e := transferSnapshot(ctx, transferFS{}, source)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(target, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = verifyTransfer(ctx, transferFS{}, transferFS{}, source, target, digest, info); e == nil {
		t.Fatal("target corruption not detected")
	}
	if e = os.WriteFile(source, []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = verifyTransfer(ctx, transferFS{}, transferFS{}, source, target, digest, info); e == nil {
		t.Fatal("source change not detected")
	}
}

func TestTransferBandwidthBudgetCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = context.WithValue(ctx, transferOptionsKey{}, transferOptions{budget: &transferBudget{rate: 1}})
	var output bytes.Buffer
	n, e := transferCopy(ctx, &output, bytes.NewReader([]byte("data")), nil)
	if !errors.Is(e, context.Canceled) || n != 0 || output.Len() != 0 {
		t.Fatalf("canceled budget copied %d: %v", n, e)
	}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	ctx = context.WithValue(ctx, transferOptionsKey{}, transferOptions{budget: &transferBudget{rate: 1}})
	n, e = transferCopy(ctx, &output, bytes.NewReader([]byte("data")), nil)
	if !errors.Is(e, context.DeadlineExceeded) || n != 0 {
		t.Fatalf("rate wait not canceled: %d %v", n, e)
	}
}

func TestTransferBandwidthIncludesHashReads(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if e := os.WriteFile(source, []byte("hash budget"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	e := transfer(ctx, "upload", []string{"--verify", "--bwlimit", "1", "@" + ticket, source, transferPath(target)}, io.Discard, io.Discard)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("expected digest throttling timeout: %v", e)
	}
	if _, e = os.Lstat(target); !os.IsNotExist(e) {
		t.Fatal("target created before throttled source digest finished")
	}
}

func TestTransferOptionCombinationsRejected(t *testing.T) {
	for _, options := range [][]string{{"--resume", "--dry-run"}, {"--resume", "--skip-identical"}, {"--bwlimit", "-1"}} {
		if _, e := enhancedTransfer(t, "upload", append(options, "missing", "source", "target")...); e == nil {
			t.Fatal("ambiguous options accepted")
		}
	}
}
