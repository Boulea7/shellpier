package sshd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/jobs"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestParseAccessMode(t *testing.T) {
	for _, v := range []string{"", "full", "sftp", "sftp-readonly"} {
		if _, err := ParseAccessMode(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"FULL", "readonly", " full", "sftp\n"} {
		if _, err := ParseAccessMode(v); err == nil {
			t.Fatal("invalid mode accepted")
		}
	}
}

func TestAccessModeEnforcedOnSSHRequests(t *testing.T) {
	for _, mode := range []AccessMode{AccessSFTP, AccessSFTPReadOnly} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			dir := t.TempDir()
			original := filepath.Join(dir, "original.txt")
			if err := os.WriteFile(original, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			manager, err := jobs.New(ctx, jobs.DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			policy, err := NewForwardPolicy([]string{target.Addr().String()})
			if err != nil {
				t.Fatal(err)
			}
			host, user := signer(t), signer(t)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan error, 1)
			go func() {
				conn, e := ln.Accept()
				if e != nil {
					done <- e
					return
				}
				done <- (&Server{Signer: host, AuthorizedKey: user.PublicKey(), WorkDir: dir, AccessMode: mode, Jobs: manager, Forward: policy}).ServeConn(ctx, conn)
			}()
			c, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(user)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey())})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { c.Close(); <-done }()
			ok, raw, err := c.SendRequest("capabilities@shellpier", true, nil)
			if err != nil || !ok {
				t.Fatal("capabilities failed")
			}
			var caps capabilities
			if json.Unmarshal(raw, &caps) != nil || caps.AccessMode != mode || !caps.SFTP || caps.SFTPReadOnly != (mode == AccessSFTPReadOnly) || caps.Exec || caps.PTY || caps.Env || caps.CWD || caps.Jobs || caps.Forward || len(caps.PTYSignals) != 0 {
				t.Fatal("capabilities overstate permission")
			}
			for _, req := range []struct {
				name    string
				payload []byte
			}{
				{"exec", ssh.Marshal(struct{ Command string }{"echo forbidden > forbidden.txt"})},
				{"shell", nil}, {"pty-req", ssh.Marshal(struct {
					Term                         string
					Columns, Rows, Width, Height uint32
					Modes                        string
				}{"xterm", 80, 24, 0, 0, "\x00"})},
				{"env", ssh.Marshal(struct{ Name, Value string }{"PATH", "forbidden"})},
				{"cwd@shellpier", ssh.Marshal(struct{ Directory string }{dir})},
				{"signal", ssh.Marshal(struct{ Name string }{"KILL"})},
				{"window-change", ssh.Marshal(struct{ Columns, Rows, Width, Height uint32 }{80, 24, 0, 0})},
			} {
				ch, requests, e := c.OpenChannel("session", nil)
				if e != nil {
					t.Fatal(e)
				}
				go ssh.DiscardRequests(requests)
				accepted, e := ch.SendRequest(req.name, true, req.payload)
				ch.Close()
				if e != nil || accepted {
					t.Fatalf("restricted request accepted: %s", req.name)
				}
			}
			ok, _, err = c.SendRequest(JobsRequest, true, []byte(`{"action":"start","operation_id":"forbidden","command":"echo forbidden"}`))
			if err != nil || ok {
				t.Fatal("jobs bypass")
			}
			if result := manager.Do(jobs.Request{Action: "list"}); len(result.Jobs) != 0 {
				t.Fatal("restricted request started a job")
			}
			if conn, e := c.Dial("tcp", target.Addr().String()); e == nil {
				conn.Close()
				t.Fatal("forward bypass")
			}
			if _, e := os.Stat(filepath.Join(dir, "forbidden.txt")); !os.IsNotExist(e) {
				t.Fatal("exec side effect")
			}
			sf, err := sftp.NewClient(c)
			if err != nil {
				t.Fatal(err)
			}
			defer sf.Close()
			file, err := sf.Open("original.txt")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil || string(data) != "unchanged" {
				t.Fatal("read failed")
			}
			if mode == AccessSFTP {
				f, e := sf.Create("allowed.txt")
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.Write([]byte("allowed"))
				if e != nil {
					t.Fatal(e)
				}
				if e = f.Close(); e != nil {
					t.Fatal(e)
				}
				return
			}
			assertReadOnlyRawOpen(t, c)
			mutations := []func() error{
				func() error {
					f, e := sf.OpenFile("original.txt", os.O_WRONLY|os.O_TRUNC)
					if e == nil {
						f.Close()
					}
					return e
				},
				func() error {
					f, e := sf.Create("new.txt")
					if e == nil {
						f.Close()
					}
					return e
				},
				func() error { return sf.Remove("original.txt") }, func() error { return sf.Rename("original.txt", "moved.txt") },
				func() error { return sf.PosixRename("original.txt", "moved.txt") }, func() error { return sf.Mkdir("newdir") },
				func() error { return sf.Chmod("original.txt", 0777) }, func() error { return sf.Chtimes("original.txt", time.Now(), time.Now()) },
				func() error { return sf.Truncate("original.txt", 0) }, func() error { return sf.Symlink("original.txt", "link.txt") }, func() error { return sf.Link("original.txt", "hardlink.txt") },
			}
			for i, mutate := range mutations {
				if e := mutate(); e == nil {
					t.Fatalf("readonly mutation %d accepted", i)
				}
			}
			var workers sync.WaitGroup
			failures := make(chan error, 32)
			for i := 0; i < 16; i++ {
				workers.Add(1)
				go func(i int) {
					defer workers.Done()
					if err := sf.Link("original.txt", fmt.Sprintf("parallel-%d", i)); err == nil {
						failures <- fmt.Errorf("parallel hardlink accepted")
					}
					if _, err := sf.Stat("original.txt"); err != nil {
						failures <- err
					}
				}(i)
			}
			workers.Wait()
			close(failures)
			for err := range failures {
				t.Fatal(err)
			}

			data, err = os.ReadFile(original)
			if err != nil || string(data) != "unchanged" {
				t.Fatal("readonly changed original")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatal("readonly created filesystem side effects")
			}
		})
	}
}

func TestOpenSSHSFTPReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix OpenSSH subprocess")
	}
	if _, err := os.Stat("/usr/bin/sftp"); err != nil {
		t.Skip("system OpenSSH unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	dir := t.TempDir()
	host := signer(t)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	user, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	if err = os.WriteFile(identity, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(dir, "known_hosts")
	keyLine := fmt.Sprintf("[127.0.0.1]:%s %s", port, ssh.MarshalAuthorizedKey(host.PublicKey()))
	if err = os.WriteFile(known, []byte(keyLine), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "ssh_config")
	settings := fmt.Sprintf("Host isolated\n HostName 127.0.0.1\n Port %s\n User shellpier\n IdentityFile %q\n UserKnownHostsFile %q\n GlobalKnownHostsFile /dev/null\n StrictHostKeyChecking yes\n IdentitiesOnly yes\n IdentityAgent none\n BatchMode yes\n PasswordAuthentication no\n KbdInteractiveAuthentication no\n ConnectTimeout 3\n", port, identity, known)
	if err = os.WriteFile(config, []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- (&Server{Signer: host, AuthorizedKey: user.PublicKey(), Shell: "sh", WorkDir: dir, AccessMode: AccessSFTPReadOnly}).ServeConn(ctx, conn)
	}()
	if err := os.WriteFile(filepath.Join(dir, "original.txt"), []byte("OpenSSH read-only data"), 0600); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "download.txt")
	second := filepath.Join(t.TempDir(), "second.txt")
	command := exec.CommandContext(ctx, "/usr/bin/sftp", "-F", config, "-b", "-", "isolated")
	command.Stdin = strings.NewReader(fmt.Sprintf("get original.txt %q\n-put %q forbidden.txt\nget original.txt %q\n", local, local, second))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenSSH SFTP failed: %v: %s", err, output)
	}
	for _, name := range []string{local, second} {
		data, e := os.ReadFile(name)
		if e != nil || string(data) != "OpenSSH read-only data" {
			t.Fatal("OpenSSH download failed")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatal("OpenSSH upload bypassed readonly")
	}

	cancel()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("OpenSSH test server did not close")
	}
}

func TestInvalidServerAccessModeFailsClosed(t *testing.T) {
	server, peer := net.Pipe()
	defer peer.Close()
	if err := (&Server{AccessMode: "typo"}).ServeConn(context.Background(), server); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("invalid server did not close transport: %v", err)
	}
}

type policyReadChannel struct {
	ssh.Channel
	reader io.Reader
}

func (c policyReadChannel) Read(p []byte) (int, error) { return c.reader.Read(p) }

func TestReadOnlySFTPFrameBoundaries(t *testing.T) {
	frame := func(name string, id uint32) []byte {
		payload := []byte{200, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(payload[1:5], id)
		binary.BigEndian.PutUint32(payload[5:9], uint32(len(name)))
		payload = append(payload, []byte(name)...)
		result := make([]byte, 4)
		binary.BigEndian.PutUint32(result, uint32(len(payload)))
		return append(result, payload...)
	}
	first := frame("hardlink@openssh.com", 41)
	second := frame("statvfs@openssh.com", 42)
	input := append(append([]byte{}, first...), second...)
	transport := &readOnlySFTPChannel{Channel: policyReadChannel{reader: bytes.NewReader(input)}}
	var output []byte
	// Small destination reads must preserve packet framing and request IDs.
	for {
		p := make([]byte, 3)
		n, err := transport.Read(p)
		output = append(output, p[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := append([]byte{}, input...)
	expected[13] = 0
	if !bytes.Equal(output, expected) {
		t.Fatal("filter changed framing, request ID, or allowed readonly extension")
	}
	for _, data := range [][]byte{{0, 0}, {0, 0, 0, 0}, {0, 16, 0, 1}, {0, 0, 0, 9, 200, 0}} {
		c := &readOnlySFTPChannel{Channel: policyReadChannel{reader: bytes.NewReader(data)}}
		if _, err := io.ReadAll(c); err == nil {
			t.Fatal("truncated or oversized frame accepted")
		}
	}
}

func assertReadOnlyRawOpen(t *testing.T, client *ssh.Client) {
	t.Helper()
	ch, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	go ssh.DiscardRequests(requests)
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"sftp"}))
	if err != nil || !ok {
		t.Fatal("raw SFTP subsystem rejected")
	}
	exchange := func(kind byte, payload []byte) []byte {
		t.Helper()
		packet := make([]byte, 5)
		binary.BigEndian.PutUint32(packet, uint32(1+len(payload)))
		packet[4] = kind
		packet = append(packet, payload...)
		if _, err := ch.Write(packet); err != nil {
			t.Fatal(err)
		}
		var header [4]byte
		if _, err := io.ReadFull(ch, header[:]); err != nil {
			t.Fatal(err)
		}
		length := binary.BigEndian.Uint32(header[:])
		if length > 1<<20 || length < 1 {
			t.Fatal("invalid SFTP response")
		}
		response := make([]byte, int(length))
		if _, err := io.ReadFull(ch, response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := exchange(1, []byte{0, 0, 0, 3}); response[0] != 2 {
		t.Fatal("SFTP version negotiation failed")
	}
	for i, flags := range []uint32{1 | 8, 1 | 16, 1 | 4, 1 | 32, 0, 0x80000001} {
		name := "original.txt"
		if flags&8 != 0 {
			name = "raw-created.txt"
		}
		id := uint32(i + 1)
		payload := ssh.Marshal(struct {
			ID           uint32
			Name         string
			Flags, Attrs uint32
		}{id, name, flags, 0})
		response := exchange(3, payload)
		if len(response) < 9 || response[0] != 101 || binary.BigEndian.Uint32(response[1:5]) != id || binary.BigEndian.Uint32(response[5:9]) != 3 {
			t.Fatalf("raw OPEN flags %x not denied with matching ID", flags)
		}
	}
}
