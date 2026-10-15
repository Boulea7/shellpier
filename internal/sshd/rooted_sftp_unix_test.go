//go:build !windows

package sshd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/sys/unix"
)

func TestRootedSFTPRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h := &rootedSFTP{root: root}
	for _, flags := range []uint32{1, 2, 1 | 2, 2 | 16} {
		done := make(chan error, 1)
		go func() {
			file, err := h.open(&sftp.Request{Filepath: "fifo", Flags: flags})
			if file != nil {
				file.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("FIFO accepted")
			}
		case <-time.After(time.Second):
			t.Fatal("FIFO open blocked")
		}
	}
}

func TestRootedSFTPSymlinkSwapCannotWriteOutside(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(dir, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h := &rootedSFTP{root: root}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Remove(filepath.Join(dir, "pivot"))
			os.Symlink("../outside", filepath.Join(dir, "pivot"))
			os.Remove(filepath.Join(dir, "pivot"))
			os.Symlink("inside", filepath.Join(dir, "pivot"))
		}
	}()
	for i := 0; i < 200; i++ {
		f, e := h.open(&sftp.Request{Filepath: "/pivot/sentinel", Flags: 2 | 8 | 16})
		if e == nil {
			f.Write([]byte("inside"))
			f.Close()
		}
	}
	close(stop)
	<-done
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("concurrent symlink swap changed outside file")
	}
}
