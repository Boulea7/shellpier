package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runFiles(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := files(context.Background(), args, strings.NewReader(""), &out, io.Discard)
	return out.String(), err
}

func TestFilesRealSFTP(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	ticket := filepath.Join(dir, "access.json")
	state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--output", ticket, "--json", "--work-dir", dir)
	input := "@" + ticket
	p := filepath.Join(dir, "中文 binary file")
	content := []byte{0, 255, 128, 'x', '\n'}
	if err := os.WriteFile(p, content, 0600); err != nil {
		t.Fatal(err)
	}
	remote := transferPath(p)
	out, err := runFiles(t, "stat", input, remote, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var stat filesMetadata
	if err = json.Unmarshal([]byte(out), &stat); err != nil || stat.Size != int64(len(content)) || stat.IsDir || stat.Symlink {
		t.Fatalf("stat: %s %v", out, err)
	}
	out, err = runFiles(t, "read", input, "--offset", "1", remote, "--json", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Data    string `json:"data"`
		HasMore bool   `json:"has_more"`
		Bytes   int    `json:"bytes"`
	}
	if err = json.Unmarshal([]byte(out), &read); err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(read.Data)
	if err != nil || !bytes.Equal(data, content[1:3]) || !read.HasMore || read.Bytes != 2 {
		t.Fatalf("read: %s", out)
	}
	out, err = runFiles(t, "read", input, remote, "--limit=5")
	if err != nil || !bytes.Equal([]byte(out), content) {
		t.Fatalf("raw: %q %v", out, err)
	}
	out, err = runFiles(t, "read", input, remote, "--offset=100", "--json")
	if err != nil || !strings.Contains(out, `"bytes":0`) {
		t.Fatalf("EOF: %s %v", out, err)
	}
	bounded := filepath.Join(dir, "bounded read")
	if err = os.WriteFile(bounded, bytes.Repeat([]byte("a"), 1024*1024+1), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = runFiles(t, "read", input, transferPath(bounded))
	if err != nil || len(out) != 1024*1024 {
		t.Fatalf("default read bound: %d %v", len(out), err)
	}
	out, err = runFiles(t, "list", input, transferPath(dir), "--limit=1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Entries []filesMetadata `json:"entries"`
		HasMore bool            `json:"has_more"`
	}
	if json.Unmarshal([]byte(out), &list) != nil || len(list.Entries) != 1 || !list.HasMore {
		t.Fatalf("list: %s", out)
	}
	newDir := filepath.Join(dir, "new directory")
	if _, err = runFiles(t, "mkdir", input, transferPath(newDir), "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatal("dry-run mkdir wrote")
	}
	if _, err = runFiles(t, "mkdir", input, transferPath(newDir)); err != nil {
		t.Fatal(err)
	}
	if _, err = runFiles(t, "mkdir", input, transferPath(newDir)); err == nil {
		t.Fatal("existing mkdir accepted")
	}
	nested := filepath.Join(newDir, "child", "leaf")
	if _, err = runFiles(t, "mkdir", input, transferPath(nested)); err == nil {
		t.Fatal("missing parent accepted")
	}
	if _, err = runFiles(t, "mkdir", input, transferPath(nested), "--parents"); err != nil {
		t.Fatal(err)
	}
	dryNested := filepath.Join(newDir, "dry", "leaf")
	if _, err = runFiles(t, "mkdir", input, transferPath(dryNested), "--parents", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(dryNested)); !os.IsNotExist(err) {
		t.Fatal("dry-run parents wrote")
	}
	if _, err = runFiles(t, "mkdir", input, transferPath(filepath.Join(p, "child")), "--parents", "--dry-run"); err == nil {
		t.Fatal("dry-run accepted file ancestor")
	}
	dashed := filepath.Join(dir, "--json")
	if err = os.WriteFile(dashed, []byte("literal"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = runFiles(t, "read", input, "--", "--json")
	if err != nil || out != "literal" {
		t.Fatalf("literal after --: %q %v", out, err)
	}
	if _, err = runFiles(t, "remove", input, transferPath(newDir)); err == nil {
		t.Fatal("nonempty directory removed")
	}
	destination := filepath.Join(dir, "renamed 中文")
	if _, err = runFiles(t, "rename", input, remote, transferPath(destination), "--dry-run", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("dry-run rename wrote")
	}
	if _, err = runFiles(t, "rename", input, remote, transferPath(destination)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runFiles(t, "rename", input, remote, transferPath(destination)); err == nil {
		t.Fatal("rename overwrote")
	}
	data, err = os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, content) {
		t.Fatal("destination changed")
	}
	if _, err = runFiles(t, "remove", input, transferPath(destination), "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(destination); err != nil {
		t.Fatal("dry-run remove wrote")
	}
	if _, err = runFiles(t, "remove", input, transferPath(destination)); err != nil {
		t.Fatal(err)
	}
	if _, err = runFiles(t, "remove", input, transferPath(nested)); err != nil {
		t.Fatal(err)
	}
	out, err = runFiles(t, "list", input, transferPath(filepath.Dir(nested)), "--json")
	if err != nil || !strings.Contains(out, `"entries":[]`) || !strings.Contains(out, `"has_more":false`) {
		t.Fatalf("empty list: %s %v", out, err)
	}

	link := filepath.Join(dir, "symbolic link")
	if err = os.Symlink(p, link); err == nil {
		out, err = runFiles(t, "stat", input, transferPath(link), "--json")
		if err != nil || !strings.Contains(out, `"symlink":true`) {
			t.Fatalf("symlink stat: %s %v", out, err)
		}
		if _, err = runFiles(t, "read", input, transferPath(link)); err == nil {
			t.Fatal("read followed symlink")
		}
		if _, err = runFiles(t, "remove", input, transferPath(link)); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(p); err != nil {
			t.Fatal("remove followed symlink")
		}
	}
	invoke(t, "stop", "--state", state)
	awaitHost(t, done, state)
}

func TestFilesValidationAndHelp(t *testing.T) {
	cases := [][]string{
		{"read", "@missing", "x", "--limit=0"}, {"read", "@missing", "x", "--limit=16777217"},
		{"read", "@missing", "x", "--offset=-1"}, {"read", "@missing", "x", "--offset=9223372036854775807"},
		{"list", "@missing", "x", "--limit=10001"}, {"list", "@missing", "x", "--limit=0"},
		{"stat", "@missing", ""}, {"stat", "@missing", "a\x00b"}, {"stat", "@missing", strings.Repeat("a", 4097)},
		{"remove", "@missing", "x", "--parents"}, {"rename", "@missing", "x"}, {"stat", "@missing", "x", "--timeout=0"},
	}
	for _, args := range cases {
		if _, err := runFiles(t, args...); err == nil || strings.Contains(err.Error(), "missing") {
			t.Fatalf("validation did not reject before ticket access: %q: %v", args, err)
		}
	}
	for _, action := range []string{"list", "stat", "read", "mkdir", "rename", "remove"} {
		_, err := runFiles(t, action, "@missing", "--help")
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help %s: %v", action, err)
		}
	}
}

func TestFilesCancellation(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	p := filepath.Join(dir, "large file")
	file, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(filesMaxRead); err != nil {
		t.Fatal(err)
	}
	file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- files(ctx, []string{"read", "@" + ticket, transferPath(p), "--limit=16777216"}, nil, io.Discard, io.Discard)
	}()
	time.AfterFunc(100*time.Millisecond, cancel)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel hung")
	}
}
