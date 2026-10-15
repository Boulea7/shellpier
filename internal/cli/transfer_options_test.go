package cli

import (
	"bytes"
	"context"
	"errors"
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
		result, e := enhancedTransfer(t, direction, "--dry-run", "--recursive", "--preserve-times", "@"+ticket, from, to)
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

func TestTransferPreserveTimesRealSFTP(t *testing.T) {
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
	result, e := enhancedTransfer(t, "upload", "--recursive", "--preserve-times", "@"+ticket, source, transferPath(remote))
	if e != nil {
		t.Fatal(e)
	}
	if result.TimesPreserved != 3 || result.MtimePrecision != "seconds" {
		t.Fatalf("summary %#v", result)
	}
	for _, p := range []string{remote, filepath.Join(remote, "sub"), filepath.Join(remote, "sub", "中文 file")} {
		info, e := os.Stat(p)
		if e != nil || !info.ModTime().Equal(modified) {
			t.Fatalf("mtime %s: %v %v", p, info, e)
		}
	}
	destination := filepath.Join(dir, "download")
	result, e = enhancedTransfer(t, "download", "--preserve-times", "@"+ticket, transferPath(filepath.Join(remote, "sub", "中文 file")), destination)
	if e != nil || result.TimesPreserved != 1 {
		t.Fatalf("single summary %#v %v", result, e)
	}
	info, e := os.Stat(destination)
	if e != nil || !info.ModTime().Equal(modified) {
		t.Fatal("download mtime lost")
	}
}

func TestTransferSkipExistingPreflightsTypeConflicts(t *testing.T) {
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
	if e := os.Mkdir(conflict, 0700); e != nil {
		t.Fatal(e)
	}
	args := []string{"--recursive", "--skip-existing", "--preserve-times", "@" + ticket, source, transferPath(dest)}
	if _, e := enhancedTransfer(t, "upload", args...); e == nil {
		t.Fatal("file/directory type conflict accepted")
	}
	if _, e := os.Lstat(filepath.Join(dest, "a-new")); !os.IsNotExist(e) {
		t.Fatal("wrote new file before conflict preflight")
	}
	if e := os.Remove(conflict); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(conflict, []byte("different foreign contents"), 0600); e != nil {
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
	if result.Files != 1 || result.SkippedFiles != 1 || result.Directories != 0 || result.TimesPreserved != 1 {
		t.Fatalf("summary %#v", result)
	}
	if content, e := os.ReadFile(conflict); e != nil || string(content) != "different foreign contents" {
		t.Fatal("existing different file was overwritten")
	}
	info, e := os.Stat(conflict)
	if e != nil || !info.ModTime().Equal(unchanged) {
		t.Fatal("skipped file metadata changed")
	}
	result, e = enhancedTransfer(t, "upload", args...)
	if e != nil || result.Files != 0 || result.SkippedFiles != 2 || result.TimesPreserved != 0 {
		t.Fatalf("repeated tree %#v %v", result, e)
	}
	result, e = enhancedTransfer(t, "upload", "--skip-existing", "--preserve-times", "@"+ticket, filepath.Join(source, "z-conflict"), transferPath(conflict))
	if e != nil || result.SkippedFiles != 1 || result.TimesPreserved != 0 {
		t.Fatalf("single skip %#v %v", result, e)
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

func TestTransferOptionCombinationsRejected(t *testing.T) {
	for _, options := range [][]string{{"--resume", "--dry-run"}, {"--resume", "--skip-existing"}, {"--bwlimit", "-1"}} {
		if _, e := enhancedTransfer(t, "upload", append(options, "missing", "source", "target")...); e == nil {
			t.Fatal("ambiguous options accepted")
		}
	}
}
