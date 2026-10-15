package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestShareRootFilesAndIsolation(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "readonly"}[readonly], func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "shared 中文 folder")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			content := []byte("shared bytes 中文\x00\xff")
			if err := os.WriteFile(filepath.Join(root, "original.bin"), content, 0600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(base, "outside.bin")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			linked := os.Symlink(base, filepath.Join(root, "outside-link")) == nil
			insideLinked := os.Symlink("original.bin", filepath.Join(root, "inside-link")) == nil
			ticket := filepath.Join(t.TempDir(), "access.json")
			args := []string{"share", "--listen", "127.0.0.1:0", "--ttl", "0", "--root", root, "--output", ticket, "--json", "--allow-jobs", "--allow-forward", "localhost:12345"}
			mode := "sftp"
			if readonly {
				mode = "sftp-readonly"
				args = append(args, "--access-mode", mode)
			}
			state, done := startHost(t, args...)
			client := hostFeatureDial(t, ticket)
			caps := hostFeatureCaps(t, client)
			if caps["file_root"] != true || caps["access_mode"] != mode || caps["sftp_read_only"] != readonly {
				t.Fatalf("root not propagated: %v", caps)
			}
			for _, name := range []string{"exec", "pty", "env", "cwd", "jobs", "forward", "reverse_forward"} {
				if caps[name] != false {
					t.Fatalf("root advertised %s: %v", name, caps)
				}
			}
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			err = session.Run("echo forbidden")
			session.Close()
			if err == nil {
				t.Fatal("root allowed exec")
			}
			if ok, _, err := client.SendRequest("jobs@shellpier", true, []byte(`{"action":"list"}`)); err != nil || ok {
				t.Fatal("root allowed jobs")
			}
			if listener, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
				listener.Close()
				t.Fatal("root allowed reverse forwarding")
			}
			sf, err := sftp.NewClient(client)
			if err != nil {
				t.Fatal(err)
			}
			if cwd, err := sf.Getwd(); err != nil || cwd != "/" {
				t.Fatalf("root cwd: %q %v", cwd, err)
			}
			file, err := sf.Open("/original.bin")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil || !bytes.Equal(data, content) {
				t.Fatalf("root read: %q %v", data, err)
			}
			for _, p := range []string{"../outside.bin", "/../outside.bin", outside} {
				if file, err := sf.Open(p); err == nil {
					file.Close()
					t.Fatalf("escaped root: %s", p)
				}
			}
			if linked {
				if file, err := sf.Open("/outside-link/outside.bin"); err == nil {
					file.Close()
					t.Fatal("symlink escaped root")
				}
				if _, err := runFiles(t, "read", "@"+ticket, "/outside-link/outside.bin"); err == nil {
					t.Fatal("file read escaped root through symlink")
				}
			}
			if insideLinked {
				got, err := runFiles(t, "read", "@"+ticket, "/inside-link")
				if err != nil || !bytes.Equal([]byte(got), content) {
					t.Fatalf("root symlink read: %q %v", got, err)
				}
			}
			local := filepath.Join(t.TempDir(), "download.bin")
			invoke(t, "download", "@"+ticket, "/original.bin", local)
			if data, err := os.ReadFile(local); err != nil || !bytes.Equal(data, content) {
				t.Fatalf("root download: %q %v", data, err)
			}
			var out bytes.Buffer
			err = Run(context.Background(), []string{"upload", "@" + ticket, local, "/uploaded.bin"}, nil, &out, io.Discard, "test")
			if readonly {
				if err == nil {
					t.Fatal("readonly root allowed upload")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(root, "uploaded.bin"))
				if err != nil || !bytes.Equal(data, content) {
					t.Fatalf("root upload: %q %v", data, err)
				}
			}
			if file, err := sf.Create("/../escape-created"); err == nil {
				file.Close()
			}
			if _, err := os.Stat(filepath.Join(base, "escape-created")); !os.IsNotExist(err) {
				t.Fatal("write escaped root")
			}
			sf.Close()
			client.Close()
			invoke(t, "stop", "--state", state)
			awaitHost(t, done, state)
			if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
				t.Fatal("outside file changed")
			}
		})
	}
}

func TestShareInvalidRootBeforeOutput(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("not directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(base, "missing"), file} {
		dir := t.TempDir()
		ticket := filepath.Join(dir, "ticket.json")
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := Run(ctx, []string{"share", "--root", root, "--listen", "invalid-listener", "--output", ticket, "--json"}, nil, &out, io.Discard, "test")
		cancel()
		if err == nil || !strings.Contains(err.Error(), "shared directory") || out.Len() != 0 {
			t.Fatalf("invalid root announced: %q %v", out.String(), err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid root created output")
		}
	}
}
