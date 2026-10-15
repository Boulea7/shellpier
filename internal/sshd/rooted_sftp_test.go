package sshd

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func rootedClient(t *testing.T, root *os.Root, mode AccessMode) *ssh.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	host, user := signer(t), signer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := ln.Accept()
		if e == nil {
			(&Server{Signer: host, AuthorizedKey: user.PublicKey(), FileRoot: root, AccessMode: mode}).ServeConn(ctx, conn)
		}
	}()
	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "ignored", Auth: []ssh.AuthMethod{ssh.PublicKeys(user)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey())})
	if err != nil {
		cancel()
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); cancel(); ln.Close(); <-done })
	return client
}
func TestRootedSFTPFilesAndIsolation(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "readonly"}[readonly], func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.WriteFile("original", []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			mode := AccessFull
			if readonly {
				mode = AccessSFTPReadOnly
			}
			c := rootedClient(t, root, mode)
			ok, payload, err := c.SendRequest("capabilities@shellpier", true, nil)
			var caps capabilities
			if err != nil || !ok || json.Unmarshal(payload, &caps) != nil || !caps.FileRoot || caps.Exec || caps.PTY || caps.Jobs || caps.Forward || caps.ReverseForward || caps.AccessMode != map[bool]AccessMode{false: AccessSFTP, true: AccessSFTPReadOnly}[readonly] {
				t.Fatal("rooted capabilities overstate access")
			}

			if ok, _, err := c.SendRequest(JobsRequest, true, []byte(`{"action":"list"}`)); err != nil || ok {
				t.Fatal("rooted host allowed jobs")
			}
			session, err := c.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Run("echo forbidden"); err == nil {
				t.Fatal("rooted host allowed exec")
			}
			session.Close()
			sf, err := sftp.NewClient(c)
			if err != nil {
				t.Fatal(err)
			}
			defer sf.Close()
			if cwd, err := sf.Getwd(); err != nil || cwd != "/" {
				t.Fatalf("virtual root: %q %v", cwd, err)
			}
			file, err := sf.Open("/original")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil || string(data) != "original" {
				t.Fatal("rooted read failed")
			}
			if readonly {
				for _, change := range []func() error{
					func() error {
						f, e := sf.Create("new")
						if e == nil {
							f.Close()
						}
						return e
					}, func() error { return sf.Link("original", "hard") }, func() error { return sf.Remove("original") }, func() error { return sf.Mkdir("dir") },
				} {
					if change() == nil {
						t.Fatal("readonly rooted mutation succeeded")
					}
				}
				return
			}
			if err := sf.Mkdir("/目录"); err != nil {
				t.Fatal(err)
			}
			f, err := sf.OpenFile("/目录/file space", os.O_CREATE|os.O_EXCL|os.O_RDWR)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("中文 content")); err != nil {
				t.Fatal(err)
			}
			f.Close()
			if err := sf.Link("/目录/file space", "/committed"); err != nil {
				t.Fatal(err)
			}
			if err := sf.PosixRename("/committed", "/renamed"); err != nil {
				t.Fatal(err)
			}
			when := time.Unix(1700000000, 0)
			if err := sf.Truncate("/renamed", 3); err != nil {
				t.Fatal(err)
			}
			if err := sf.Chtimes("/renamed", when, when); err != nil {
				t.Fatal(err)
			}
			if info, err := sf.Stat("/renamed"); err != nil || info.Size() != 3 || info.ModTime().Unix() != when.Unix() {
				t.Fatalf("setstat failed: %v", err)
			}
			if list, err := sf.ReadDir("/"); err != nil || len(list) != 3 {
				t.Fatalf("list failed: %v", err)
			}
			if err := sf.Symlink("original", "unsupported-link"); err == nil {
				t.Fatal("symlink creation unexpectedly enabled")
			}
			if err := sf.Remove("/renamed"); err != nil {
				t.Fatal(err)
			}
			if err := sf.Remove("/目录/file space"); err != nil {
				t.Fatal(err)
			}
			if err := sf.RemoveDirectory("/目录"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRootedSFTPTraversalAndHandleAnchor(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "root")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.WriteFile(outside, []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h := &rootedSFTP{root: root}
	if err := root.WriteFile("inside", []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "/../outside"} {
		if f, e := h.Fileread(&sftp.Request{Filepath: name, Flags: 1}); e == nil {
			f.(io.Closer).Close()
			t.Fatal("parent traversal allowed")
		}
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{`C:\outside`, `/C:/outside`, `//server/share/file`, `NUL`, `CONOUT$`, `inside:stream`, `\\server\share\file`} {
			if f, e := h.Fileread(&sftp.Request{Filepath: name, Flags: 1}); e == nil {
				f.(io.Closer).Close()
				t.Fatalf("Windows special path accepted: %q", name)
			}
		}
		return
	}
	if err := os.Symlink("../outside", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if f, e := h.Fileread(&sftp.Request{Filepath: "/escape", Flags: 1}); e == nil {
		f.(io.Closer).Close()
		t.Fatal("symlink escape allowed")
	}
	if err := os.Symlink("inside", filepath.Join(dir, "safe")); err != nil {
		t.Fatal(err)
	}
	f, err := h.Fileread(&sftp.Request{Filepath: "/safe", Flags: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.(io.Closer).Close()
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "inside"), []byte("replacement"), 0600)
	data, err := root.ReadFile("inside")
	if err != nil || string(data) != "inside" {
		t.Fatal("root followed replaced directory pathname")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			root.Rename("inside", "swapped")
			root.Rename("swapped", "inside")
		}
	}()
	for i := 0; i < 100; i++ {
		f, e := h.Fileread(&sftp.Request{Filepath: "safe", Flags: 1})
		if e == nil {
			b := make([]byte, 32)
			n, _ := f.ReadAt(b, 0)
			f.(io.Closer).Close()
			if string(b[:n]) != "inside" {
				t.Fatal("rename race read outside root")
			}
		}
	}
	wg.Wait()
}

func TestRootedSFTPAppendIgnoresWriteOffset(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("append.txt", []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	sf, err := sftp.NewClient(rootedClient(t, root, AccessFull))
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	file, err := sf.OpenFile("/append.txt", os.O_WRONLY|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("-one"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("-two"), 12345); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := root.ReadFile("append.txt")
	if err != nil || string(data) != "original-one-two" {
		t.Fatalf("APPEND did not preserve existing bytes (size %d): %v", len(data), err)
	}
}

func TestRootedSFTPRejectsOversizedSetstat(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	root.WriteFile("original", []byte("unchanged"), 0600)
	h := &rootedSFTP{root: root}
	for _, size := range []uint64{1 << 63, ^uint64(0)} {
		attrs := make([]byte, 8)
		binary.BigEndian.PutUint64(attrs, size)
		if err := h.Filecmd(&sftp.Request{Method: "Setstat", Filepath: "original", Flags: 1, Attrs: attrs}); err == nil {
			t.Fatal("oversized SETSTAT accepted")
		}
	}
	data, err := root.ReadFile("original")
	if err != nil || string(data) != "unchanged" {
		t.Fatal("oversized truncate changed file")
	}
}
