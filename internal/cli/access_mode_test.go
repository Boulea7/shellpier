package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestShareAccessModesEndToEnd(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, mode := range []string{"full", "sftp", "sftp-readonly"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ticket := filepath.Join(t.TempDir(), "access.json")
			original := filepath.Join(dir, "原始 file.txt")
			content := []byte("中文 file content\n")
			if err := os.WriteFile(original, content, 0600); err != nil {
				t.Fatal(err)
			}
			shell := "sh"
			if runtime.GOOS == "windows" {
				shell = "cmd"
			}
			args := []string{"share", "--listen", "127.0.0.1:0", "--ttl", "0", "--shell", shell, "--work-dir", dir, "--output", ticket, "--json"}
			if mode != "full" {
				args = append(args, "--access-mode", mode)
			}
			state, done := startHost(t, args...)
			client := hostFeatureDial(t, ticket)
			caps := hostFeatureCaps(t, client)
			if caps["access_mode"] != mode || caps["sftp"] != true || caps["sftp_read_only"] != (mode == "sftp-readonly") {
				t.Fatal("CLI did not propagate file access policy")
			}
			if mode == "full" {
				if caps["exec"] != true {
					t.Fatal("default full mode lost exec")
				}
				var out, diagnostic bytes.Buffer
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := Run(ctx, []string{"exec", "@" + ticket, "--command", "echo access-mode-command"}, nil, &out, &diagnostic, "test"); err != nil || !strings.Contains(out.String(), "access-mode-command") {
					t.Fatalf("default full exec failed: %v", err)
				}
			} else {
				for _, name := range []string{"exec", "pty", "env", "cwd", "jobs", "forward"} {
					if caps[name] != false {
						t.Fatalf("restricted capability %s enabled", name)
					}
				}
				var out, diagnostic bytes.Buffer
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := Run(ctx, []string{"exec", "@" + ticket, "--command", "echo forbidden > forbidden.txt"}, nil, &out, &diagnostic, "test")
				cancel()
				if err == nil {
					t.Fatal("restricted CLI exec succeeded")
				}

				for _, request := range []struct {
					name    string
					payload []byte
				}{
					{"exec", ssh.Marshal(struct{ Command string }{"echo forbidden > forbidden.txt"})},
					{"pty-req", ssh.Marshal(struct {
						Term                         string
						Columns, Rows, Width, Height uint32
						Modes                        string
					}{"xterm", 80, 24, 0, 0, "\x00"})},
					{"env", ssh.Marshal(struct{ Name, Value string }{"PATH", "forbidden"})},
					{"cwd@shellpier", ssh.Marshal(struct{ Directory string }{dir})}, {"shell", nil},
				} {
					ch, requests, err := client.OpenChannel("session", nil)
					if err != nil {
						t.Fatal(err)
					}
					go ssh.DiscardRequests(requests)
					ok, err := ch.SendRequest(request.name, true, request.payload)
					ch.Close()
					if err != nil || ok {
						t.Fatalf("restricted %s request accepted", request.name)
					}
				}
				if ok, _, err := client.SendRequest("jobs@shellpier", true, []byte(`{"action":"start","operation_id":"forbidden","command":"echo forbidden"}`)); err != nil || ok {
					t.Fatal("restricted jobs request accepted")
				}
				if conn, err := client.Dial("tcp", "127.0.0.1:1"); err == nil {
					conn.Close()
					t.Fatal("restricted forwarding accepted")
				}
			}
			sf, err := sftp.NewClient(client)
			if err != nil {
				t.Fatal(err)
			}
			f, err := sf.Open("原始 file.txt")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil || !bytes.Equal(data, content) {
				t.Fatal("SFTP read failed")
			}
			local := filepath.Join(t.TempDir(), "download.txt")
			invoke(t, "download", "@"+ticket, "原始 file.txt", local)
			data, err = os.ReadFile(local)
			if err != nil || !bytes.Equal(data, content) {
				t.Fatal("CLI download failed")
			}
			if mode == "sftp-readonly" {
				var out, diagnostic bytes.Buffer
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := Run(ctx, []string{"upload", "@" + ticket, local, "uploaded.txt"}, nil, &out, &diagnostic, "test")
				cancel()
				if err == nil {
					t.Fatal("readonly CLI upload succeeded")
				}
				for i, change := range []func() error{func() error { return sf.Mkdir("newdir") }, func() error { return sf.Rename("原始 file.txt", "moved.txt") }, func() error { return sf.Remove("原始 file.txt") }} {
					if err := change(); err == nil {
						t.Fatalf("readonly mutation %d succeeded", i)
					}
				}
			} else {
				invoke(t, "upload", "@"+ticket, local, "uploaded.txt")
				data, err := os.ReadFile(filepath.Join(dir, "uploaded.txt"))
				if err != nil || !bytes.Equal(data, content) {
					t.Fatal("writable CLI upload failed")
				}
				if err := sf.Remove("uploaded.txt"); err != nil {
					t.Fatal(err)
				}
			}
			sf.Close()
			client.Close()
			data, err = os.ReadFile(original)
			if err != nil || !bytes.Equal(data, content) {
				t.Fatal("original file changed")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatal("restricted requests changed filesystem")
			}
			invoke(t, "stop", "--state", state)
			awaitHost(t, done, state)
		})
	}
}

func TestShareAccessModeRejectsBeforeOutput(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	cases := [][]string{{"--access-mode", "invalid"}, {"--access-mode", "sftp", "--allow-jobs"}, {"--access-mode", "sftp-readonly", "--allow-jobs"}, {"--access-mode", "sftp", "--allow-forward", "127.0.0.1:12345"}, {"--access-mode", "sftp-readonly", "--allow-forward", "127.0.0.1:12345"}}
	for _, arguments := range cases {
		dir := t.TempDir()
		ticket := filepath.Join(dir, "access.json")
		args := append([]string{"share", "--listen", "invalid-listener-address", "--output", ticket, "--json"}, arguments...)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var out, diagnostic bytes.Buffer
		err := Run(ctx, args, nil, &out, &diagnostic, "test")
		cancel()
		if err == nil || !strings.Contains(err.Error(), "access mode") && !strings.Contains(err.Error(), "access-mode") {
			t.Fatalf("access policy not rejected before network setup: %v", err)
		}
		if out.Len() != 0 {
			t.Fatal("invalid access mode emitted output")
		}
		entries, e := os.ReadDir(dir)
		if e != nil || len(entries) != 0 {
			t.Fatal("invalid access mode created files")
		}
	}
}
