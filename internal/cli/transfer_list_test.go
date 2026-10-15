package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTransferListParsing(t *testing.T) {
	for _, tc := range []struct {
		input string
		zero  bool
		want  []string
	}{
		{"./a\r\nb/c\n a\n#literal\n./a\n", false, []string{" a", "#literal", "a", "b/c"}},
		{"./a\x00b/line\nbreak\x00", true, []string{"a", "b/line\nbreak"}},
		{"", false, nil},
	} {
		got, err := readTransferList(context.Background(), "-", strings.NewReader(tc.input), tc.zero, 100, 32)
		if err != nil || strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("parse %q = %q %v", tc.input, got, err)
		}
	}
	for _, input := range []string{"../x", "x/../a", "/absolute", "C:/drive", "x\\a", "a//b", "a/./b", "A/one\na/two", "a\nA", "CON", "a\x00b"} {
		if _, err := readTransferList(context.Background(), "-", strings.NewReader(input), false, 100, 32); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	for _, tc := range []struct {
		input        string
		files, depth int
	}{{"a\nb", 1, 32}, {"a/b", 100, 0}, {strings.Repeat("x", 4097), 100, 32}, {strings.Repeat("x", transferListMaxBytes+1), 100, 32}} {
		if _, err := readTransferList(context.Background(), "-", strings.NewReader(tc.input), false, tc.files, tc.depth); err == nil {
			t.Fatal("accepted oversized input")
		}
	}
}

func listTransfer(t *testing.T, direction, ticket, source, dest, list string, options ...string) (transferSummary, error) {
	t.Helper()
	args := append([]string{"--files-from", "-", "--json"}, options...)
	args = append(args, "@"+ticket, source, dest)
	var out bytes.Buffer
	err := transferWithInput(context.Background(), direction, args, strings.NewReader(list), &out, io.Discard)
	var result transferSummary
	if err == nil {
		if e := json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
	}
	return result, err
}

func TestTransferListRoundtrip(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	names := []string{" a", "#literal", "sub/中文 file"}
	if runtime.GOOS != "windows" {
		names = append(names, "line\nbreak")
	}
	for _, name := range append(append([]string{}, names...), "unselected") {
		if err := os.WriteFile(filepath.Join(source, name), []byte("binary\x00"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	list := strings.Join(names, "\x00") + "\x00"
	remote := filepath.Join(dir, "remote")
	if err := os.Mkdir(remote, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := listTransfer(t, "upload", ticket, source, transferPath(remote), list, "--from0", "--preserve-times")
	if err != nil || result.Files != len(names) || result.Directories != 1 {
		t.Fatalf("upload %+v %v", result, err)
	}
	local := filepath.Join(dir, "download")
	result, err = listTransfer(t, "download", ticket, transferPath(remote), local, list, "--from0")
	if err != nil || result.Files != len(names) {
		t.Fatalf("download %+v %v", result, err)
	}
	for _, name := range names {
		got, e := os.ReadFile(filepath.Join(local, name))
		if e != nil || string(got) != "binary\x00"+name {
			t.Fatalf("file %q: %q %v", name, got, e)
		}
	}
	if _, err := os.Stat(filepath.Join(local, "unselected")); !os.IsNotExist(err) {
		t.Fatal("unselected copied")
	}
	if _, err := listTransfer(t, "upload", ticket, source, transferPath(remote), list, "--from0"); err == nil {
		t.Fatal("overwrote existing")
	}
	result, err = listTransfer(t, "upload", ticket, source, transferPath(remote), list, "--from0", "--skip-existing")
	if err != nil || result.Files != 0 || result.SkippedFiles != len(names) {
		t.Fatalf("skip %+v %v", result, err)
	}
}

func TestTransferListPreflightAndDryRun(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest")
	for _, list := range []string{"a\nmissing", "a\n"} {
		result, err := listTransfer(t, "upload", ticket, source, transferPath(dest), list, "--dry-run")
		if strings.Contains(list, "missing") {
			if err == nil {
				t.Fatal("missing accepted")
			}
		} else if err != nil || !result.DryRun || result.PlannedBytes != 1 {
			t.Fatalf("dryrun %+v %v", result, err)
		}
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Fatal("preflight created destination")
		}
	}
	if _, err := listTransfer(t, "upload", ticket, source, transferPath(dest), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("empty list created destination")
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dest, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := listTransfer(t, "upload", ticket, source, transferPath(dest), "a", "--skip-existing"); err == nil {
		t.Fatal("directory conflict skipped")
	}
	if err := os.Symlink(source, filepath.Join(source, "link")); err == nil {
		if _, err := listTransfer(t, "upload", ticket, source, transferPath(dest), "link/a"); err == nil {
			t.Fatal("source intermediate symlink accepted")
		}
		if _, err := listTransfer(t, "upload", ticket, filepath.Join(source, "link"), transferPath(dest), "a"); err == nil {
			t.Fatal("source root symlink accepted")
		}
	}
}

func TestTransferListValidationAndBlockedCancel(t *testing.T) {
	for _, options := range [][]string{{"--from0"}, {"--files-from", "-", "--recursive"}, {"--files-from", "-", "--exclude", "x"}, {"--files-from", "-", "--resume"}, {"--files-from", "-", "--resume-state", "x"}} {
		args := append(options, "missing-ticket", "source", "dest")
		err := transferWithInput(context.Background(), "upload", args, strings.NewReader("a"), io.Discard, io.Discard)
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("validation %v: %v", options, err)
		}
	}
	for _, direction := range []string{"upload", "download"} {
		localSource, localDest := "source", "dest"
		if direction == "upload" {
			localSource = "-"
		} else {
			localDest = "-"
		}
		err := transferWithInput(context.Background(), direction, []string{"--files-from", "-", "missing-ticket", localSource, localDest}, strings.NewReader("a"), io.Discard, io.Discard)
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("payload conflict %s: %v", direction, err)
		}
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- transferWithInput(ctx, "upload", []string{"--files-from", "-", "missing-ticket", "source", "dest"}, reader, io.Discard, io.Discard)
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("list stdin ignored cancellation")
	}
}

func TestTransferListRootAndReadonly(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, mode := range []string{"sftp", "sftp-readonly"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "sub", "source"), []byte("root"), 0600); err != nil {
				t.Fatal(err)
			}
			ticket := filepath.Join(t.TempDir(), "access.json")
			state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--root", root, "--access-mode", mode, "--output", ticket, "--json")
			defer func() { invoke(t, "stop", "--state", state); awaitHost(t, done, state) }()
			local := filepath.Join(t.TempDir(), "copy")
			if result, err := listTransfer(t, "download", ticket, "/", local, "./sub/source"); err != nil || result.Files != 1 {
				t.Fatalf("root download %+v %v", result, err)
			}
			if err := os.WriteFile(filepath.Join(local, "sub", "new"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := listTransfer(t, "upload", ticket, local, "/", "sub/new")
			if (mode == "sftp-readonly") == (err == nil) {
				t.Fatalf("root mode %s upload %v", mode, err)
			}
			if mode == "sftp-readonly" {
				if _, err := os.Stat(filepath.Join(root, "sub", "new")); !os.IsNotExist(err) {
					t.Fatal("readonly wrote file")
				}
			}
		})
	}
}

func TestTransferListNamedInputAndTargetSymlink(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "sub", "data"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "list")
	if err := os.WriteFile(manifest, []byte("./sub/data\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest")
	if err := transferWithInput(context.Background(), "upload", []string{"--files-from", manifest, "@" + ticket, source, transferPath(dest)}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	linked := filepath.Join(dir, "linked")
	if err := os.Symlink(outside, linked); err == nil {
		for _, target := range []string{linked, linked + string(os.PathSeparator)} {
			if _, err := listTransfer(t, "upload", ticket, source, transferPath(target), "sub/data"); err == nil {
				t.Fatal("destination root symlink accepted")
			}
		}
		existing := filepath.Join(dir, "existing")
		if err := os.Mkdir(existing, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(existing, "sub")); err != nil {
			t.Fatal(err)
		}
		if _, err := listTransfer(t, "upload", ticket, source, transferPath(existing), "sub/data"); err == nil {
			t.Fatal("destination intermediate symlink accepted")
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatal("symlink target changed")
		}
	}
}

type transferListActionWriter struct {
	action func() error
	done   bool
}

func (w *transferListActionWriter) Write(p []byte) (int, error) {
	if !w.done {
		w.done = true
		if err := w.action(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func TestTransferListRechecksPathsDuringExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires unprivileged directory symlinks")
	}
	dir, ticket := slowTransferEndpoint(t)
	source := filepath.Join(dir, "source")
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "sub/b"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("safe"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "b"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := &transferListActionWriter{action: func() error {
		if err := os.Rename(filepath.Join(source, "sub"), filepath.Join(source, "saved")); err != nil {
			return err
		}
		return os.Symlink(outside, filepath.Join(source, "sub"))
	}}
	dest := filepath.Join(dir, "dest")
	err := transferWithInputAndProgress(context.Background(), "upload", []string{"--files-from", "-", "--progress", "jsonl", "@" + ticket, source, transferPath(dest)}, strings.NewReader("a\nsub/b"), io.Discard, io.Discard, changed)
	if err == nil || !strings.Contains(err.Error(), "source directory changed after planning") {
		t.Fatalf("execution path replacement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sub", "b")); !os.IsNotExist(err) {
		t.Fatal("copied through replaced directory")
	}
}
