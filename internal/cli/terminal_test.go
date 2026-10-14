package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/crypto/ssh"
)

func TestTerminalRingBoundsCursorsAndWait(t *testing.T) {
	ring := newTerminalRing()
	payload := bytes.Repeat([]byte("x"), terminalCapacity+123)
	if _, err := ring.Write(payload); err != nil {
		t.Fatal(err)
	}
	result, err := ring.read(context.Background(), 0, 64, 0)
	if err != nil || result.Dropped != 123 || result.NextCursor != 187 || len(result.Data) != 64 {
		t.Fatalf("bad cursor result %#v %v", result, err)
	}
	if _, err = ring.read(context.Background(), uint64(len(payload)+1), 64, 0); err == nil {
		t.Fatal("future cursor accepted")
	}
	done := make(chan terminalReadResult, 1)
	go func() { r, _ := ring.read(context.Background(), uint64(len(payload)), 64, time.Second); done <- r }()
	ring.Write([]byte("ready"))
	select {
	case r := <-done:
		if string(r.Data) != "ready" {
			t.Fatalf("bad wake %#v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not wake")
	}
	ring.finish(7)
	snapshot := ring.snapshot()
	if !snapshot.Closed || snapshot.ExitCode == nil || *snapshot.ExitCode != 7 || len(snapshot.Data) != terminalCapacity {
		t.Fatal("bad closed snapshot")
	}
	r, err := terminalReadSnapshot(snapshot, 0, 80)
	if err != nil || r.Dropped != 128 || r.NextCursor != 208 {
		t.Fatalf("bad offline snapshot %#v %v", r, err)
	}
	if _, err = ring.Write([]byte("late")); err == nil {
		t.Fatal("closed output accepted")
	}
}
func TestTerminalRingCancellationAndConcurrentOutput(t *testing.T) {
	ring := newTerminalRing()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ring.read(ctx, 0, 64, time.Second); err == nil {
		t.Fatal("cancel ignored")
	}
	var writers sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for n := 0; n < 100; n++ {
				ring.Write(bytes.Repeat([]byte("z"), 1024))
			}
		}()
	}
	writers.Wait()
	if ring.snapshot().NextCursor != 409600 {
		t.Fatal("output lost")
	}
}
func terminalTestState(t *testing.T) (terminalState, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "terminal")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := platform.SecureDir(dir); err != nil {
		t.Fatal(err)
	}
	id, _ := secret()
	token, _ := secret()
	state := terminalState{Version: 1, Kind: "shellpier-terminal", ID: id, Secret: token, ExpiresAt: time.Now().Add(time.Minute), Term: "xterm-256color", Cols: 80, Rows: 24}
	path := filepath.Join(dir, "state.json")
	if err := writeJSONPrivate(path, state); err != nil {
		t.Fatal(err)
	}
	return state, path
}

type terminalFakePeer struct {
	ring   *terminalRing
	closed chan struct{}
	once   sync.Once
	writes atomic.Int32
	cols   atomic.Int32
	rows   atomic.Int32
}

func (p *terminalFakePeer) Write(data []byte) (int, error) {
	p.writes.Add(1)
	return p.ring.Write(data)
}
func (p *terminalFakePeer) Resize(cols, rows int) error {
	p.cols.Store(int32(cols))
	p.rows.Store(int32(rows))
	return nil
}
func (p *terminalFakePeer) Signal(signal string) error {
	if signal == "USR1" {
		return io.ErrClosedPipe
	}
	return nil
}
func (p *terminalFakePeer) Close() error { p.once.Do(func() { close(p.closed) }); return nil }
func (p *terminalFakePeer) Wait() error  { <-p.closed; return io.EOF }
func TestTerminalControlTokenExactWritesSnapshotAndUnknownFiles(t *testing.T) {
	state, path := terminalTestState(t)
	ring := newTerminalRing()
	peer := &terminalFakePeer{ring: ring, closed: make(chan struct{})}
	runtime := newTerminalRuntime(context.Background(), state, path, peer, ring)
	server := httptest.NewServer(runtime)
	defer server.Close()
	defer runtime.end(-1)
	state.ControlURL = server.URL
	runtime.state = state
	if err := terminalReplacePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/write", strings.NewReader(`{"data":"YQ=="}`))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 || peer.writes.Load() != 0 {
		t.Fatal("control token not enforced")
	}
	sentinel := filepath.Join(filepath.Dir(path), "unrelated.txt")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	var out bytes.Buffer
	if err = terminalControl(context.Background(), "write", []string{"--state", path, "--text", "exact\x00bytes", "--json"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if peer.writes.Load() != 1 || string(ring.snapshot().Data) != "exact\x00bytes" {
		t.Fatal("write modified or replayed bytes")
	}
	out.Reset()
	if err = terminalControl(context.Background(), "resize", []string{"--state", path, "--cols", "91", "--rows", "37", "--json"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if peer.cols.Load() != 91 || peer.rows.Load() != 37 {
		t.Fatal("resize not applied")
	}
	if err = terminalControl(context.Background(), "signal", []string{"--state", path, "--signal", "USR1"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("failed signal reported success")
	}
	out.Reset()
	if err = terminalControl(context.Background(), "close", []string{"--state", path, "--json"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), state.Secret) {
		t.Fatal("metadata leaked token")
	}
	server.Close()
	out.Reset()
	if err = terminalControl(context.Background(), "read", []string{"--state", path, "--cursor", "0"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var read terminalReadResult
	if json.Unmarshal(out.Bytes(), &read) != nil || !read.Closed || string(read.Data) != "exact\x00bytes" {
		t.Fatal("closed snapshot unavailable")
	}
	if b, _ := os.ReadFile(sentinel); string(b) != "keep" {
		t.Fatal("unrelated file removed")
	}
}
func TestTerminalInvalidOptionsAndPrivateState(t *testing.T) {
	for _, args := range [][]string{{"open", "--cols", "1001", "--out", "x", "ticket"}, {"open", "--term", "bad term", "--out", "x", "ticket"}, {"open", "--timeout", "0", "--out", "x", "ticket"}, {"read", "--text", "ignored", "--state", "x"}, {"write", "--state", "x", "--text", "a", "--file", "-"}} {
		if err := terminal(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err == nil {
			t.Fatalf("invalid accepted: %v", args)
		}
	}
	state, path := terminalTestState(t)
	state.ControlURL = "http://example.com:80"
	terminalReplacePrivate(path, state)
	if _, err := terminalReadState(path, false); err == nil {
		t.Fatal("nonloopback endpoint accepted")
	}
	if err := terminalServeEntry(context.Background(), []string{"--state", path}, strings.NewReader("not a token\n"), io.Discard, io.Discard); err == nil {
		t.Fatal("hidden worker accepted bad handshake")
	}
}
func terminalFakeSSH(t *testing.T) (access.Ticket, *atomic.Int32) {
	return terminalFakeSSHMode(t, false)
}
func terminalFakeSSHMode(t *testing.T, fast bool) (access.Ticket, *atomic.Int32) {
	t.Helper()
	ticket, host, err := access.NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(host.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ticket.Addresses = []string{listener.Addr().String()}
	starts := &atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				stop := context.AfterFunc(ctx, func() { raw.Close() })
				defer stop()
				config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
				config.AddHostKey(signer)
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for pending := range channels {
					channel, requests, err := pending.Accept()
					if err != nil {
						return
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							switch request.Type {
							case "pty-req", "cwd@shellpier", "env":
								request.Reply(true, nil)
							case "shell", "exec":
								starts.Add(1)
								request.Reply(true, nil)
								if fast {
									channel.Write([]byte("finished\n"))
									channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
									channel.Close()
									return
								}
								go func() { io.Copy(channel, channel) }()
							case "window-change":
								request.Reply(true, nil)
							case "signal":
								var signal struct{ Signal string }
								ssh.Unmarshal(request.Payload, &signal)
								request.Reply(signal.Signal != "USR1", nil)
							default:
								request.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return ticket, starts
}
func TestTerminalSSHPTYRequestsAndNoReplay(t *testing.T) {
	ticket, starts := terminalFakeSSH(t)
	state, path := terminalTestState(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ring := newTerminalRing()
	peer, err := terminalSSHConnect(ctx, state, terminalConfig{Ticket: ticket, Retry: access.DefaultRetryOptions(), Env: []string{"SHELLPIER_TEST=1"}}, ring)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err = peer.Resize(91, 37); err != nil {
		t.Fatal(err)
	}
	if err = peer.Signal("USR1"); err == nil {
		t.Fatal("unsupported SSH signal was reported successful")
	}
	if _, err = peer.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	read, err := ring.read(ctx, 0, 100, time.Second)
	if err != nil || string(read.Data) != "hello" {
		t.Fatalf("echo missing %q %v", read.Data, err)
	}
	runtime := newTerminalRuntime(ctx, state, path, peer, ring)
	runtime.end(-1)
	if err = runtime.snapshotError(); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("session replayed: %d", starts.Load())
	}
}
func TestTerminalServePrivateHandshakeAndTTL(t *testing.T) {
	ticket, starts := terminalFakeSSH(t)
	state, path := terminalTestState(t)
	state.ExpiresAt = time.Now().Add(500 * time.Millisecond)
	terminalReplacePrivate(path, state)
	if err := writeJSONPrivate(filepath.Join(filepath.Dir(path), "config.json"), terminalConfig{Ticket: ticket, Retry: access.DefaultRetryOptions()}); err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	defer outputReader.Close()
	defer outputWriter.Close()
	done := make(chan error, 1)
	go func() {
		defer outputWriter.Close()
		done <- terminalServeEntry(context.Background(), []string{"--state", path}, inputReader, outputWriter, io.Discard)
	}()
	io.WriteString(inputWriter, state.Secret+"\n")
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(outputReader).Decode(&ready); err != nil || !ready.Ready {
		t.Fatalf("not ready: %v", err)
	}
	io.WriteString(inputWriter, "commit\n")
	inputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("worker did not expire")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(path), "config.json")); !os.IsNotExist(err) {
		t.Fatal("ticket remained after connection")
	}
	snapshot, err := terminalSnapshotFile(filepath.Join(filepath.Dir(path), "snapshot.json"))
	if err != nil || !snapshot.Closed || starts.Load() != 1 {
		t.Fatalf("TTL cleanup failure %v", err)
	}
}

func TestTerminalStartupCleanupPreservesReplacedPrivateFiles(t *testing.T) {
	state, path := terminalTestState(t)
	dir := filepath.Dir(path)
	created, _ := terminalFileIdentity(dir)
	original, _ := terminalFileIdentity(path)
	known := map[string]os.FileInfo{"state.json": original}
	replacement := filepath.Join(dir, "replacement.json")
	if err := writePrivate(replacement, []byte(`{"unrelated":"keep"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "snapshot.json", "other.txt"} {
		if err := writePrivate(filepath.Join(dir, name), []byte("keep")); err != nil {
			t.Fatal(err)
		}
	}
	terminalRemoveStartup(dir, created, known, state)
	for _, name := range []string{"state.json", "config.json", "snapshot.json", "other.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("replacement or unknown %s removed", name)
		}
	}
}
func TestTerminalConcurrentResizeReadStatus(t *testing.T) {
	state, path := terminalTestState(t)
	ring := newTerminalRing()
	peer := &terminalFakePeer{ring: ring, closed: make(chan struct{})}
	runtime := newTerminalRuntime(context.Background(), state, path, peer, ring)
	server := httptest.NewServer(runtime)
	defer server.Close()
	defer runtime.end(-1)
	state.ControlURL = server.URL
	runtime.state = state
	terminalReplacePrivate(path, state)
	var work sync.WaitGroup
	failures := make(chan error, 30)
	for i := 0; i < 10; i++ {
		for _, mode := range []string{"resize", "read", "status"} {
			work.Add(1)
			go func(mode string) {
				defer work.Done()
				args := []string{"--state", path, "--json"}
				if mode == "resize" {
					args = append(args, "--cols", "93", "--rows", "27")
				}
				if err := terminalControl(context.Background(), mode, args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
					failures <- err
				}
			}(mode)
		}
	}
	work.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
func terminalBuildTestBinary(t *testing.T) string {
	t.Helper()
	name := "shellpier"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/shellpier")
	command.Dir = filepath.Join("..", "..")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build terminal test executable: %s %v", out, err)
	}
	return binary
}
func TestTerminalRealBinaryFastOpenAndSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("real binary lifecycle")
	}
	binary := terminalBuildTestBinary(t)
	ticket, starts := terminalFakeSSHMode(t, true)
	encoded, err := ticket.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		dir := filepath.Join(t.TempDir(), "terminal")
		command := exec.CommandContext(ctx, binary, "terminal", "open", encoded, "--out", dir, "--command", "true", "--json")
		data, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("fast open %d failed: %s %v", i, data, err)
		}
		var meta terminalMetadata
		if json.Unmarshal(data, &meta) != nil || meta.StatePath == "" {
			t.Fatalf("invalid fast open metadata: %s", data)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			snapshot, e := terminalSnapshotFile(filepath.Join(dir, "snapshot.json"))
			if e == nil {
				if !snapshot.Closed || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 || string(snapshot.Data) != "finished\n" {
					t.Fatalf("fast command output invalid: %#v", snapshot)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("fast command snapshot unavailable: %v", e)
			}
			time.Sleep(10 * time.Millisecond)
		}
		var read bytes.Buffer
		if err = terminalControl(context.Background(), "read", []string{"--state", meta.StatePath}, strings.NewReader(""), &read, io.Discard); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
			t.Fatal("worker access ticket retained")
		}
	}
	if starts.Load() != 10 {
		t.Fatalf("fast operations replayed: %d", starts.Load())
	}
}

func TestTerminalRealBinaryPersistentAcrossCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("real binary lifecycle")
	}
	binary := terminalBuildTestBinary(t)
	ticket, starts := terminalFakeSSH(t)
	encoded, err := ticket.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "terminal")
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, binary, append([]string{"terminal"}, args...)...).CombinedOutput()
	}
	data, err := run("open", encoded, "--out", dir, "--timeout", "30s", "--json")
	if err != nil {
		t.Fatalf("persistent open failed %s %v", data, err)
	}
	var meta terminalMetadata
	if json.Unmarshal(data, &meta) != nil {
		t.Fatal("invalid metadata")
	}
	t.Cleanup(func() {
		terminalControl(context.Background(), "close", []string{"--state", meta.StatePath}, strings.NewReader(""), io.Discard, io.Discard)
	})
	data, err = run("write", "--state", meta.StatePath, "--text", "bytes without implicit newline", "--json")
	if err != nil {
		t.Fatalf("cross-call write %s %v", data, err)
	}
	data, err = run("read", "--state", meta.StatePath, "--wait", "1s")
	if err != nil {
		t.Fatalf("cross-call read %s %v", data, err)
	}
	var result terminalReadResult
	if json.Unmarshal(data, &result) != nil || string(result.Data) != "bytes without implicit newline" {
		t.Fatalf("cross-call output %q", data)
	}
	if data, err = run("resize", "--state", meta.StatePath, "--cols", "101", "--rows", "43", "--json"); err != nil {
		t.Fatalf("cross-call resize %s %v", data, err)
	}
	if data, err = run("signal", "--state", meta.StatePath, "--signal", "USR1", "--json"); err == nil {
		t.Fatalf("unsupported signal reported success %s", data)
	}
	data, err = run("close", "--state", meta.StatePath, "--json")
	if err != nil {
		t.Fatalf("cross-call close %s %v", data, err)
	}
	data, err = run("status", "--state", meta.StatePath, "--json")
	if err != nil {
		t.Fatalf("closed status %s %v", data, err)
	}
	var closed terminalMetadata
	if json.Unmarshal(data, &closed) != nil || !closed.Closed || closed.Cols != 101 || closed.Rows != 43 {
		t.Fatalf("bad closed status %s", data)
	}
	if starts.Load() != 1 {
		t.Fatalf("remote terminal replayed %d", starts.Load())
	}
	state, err := terminalReadState(meta.StatePath, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, state.ControlURL+"/status", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+state.Secret)
		response, e := http.DefaultClient.Do(request)
		cancel()
		if e != nil {
			break
		}
		response.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("closed worker control listener remains active")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminalNativePTYAcrossCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("native PTY integration")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell command; Windows ConPTY is tested separately")
	}
	t.Setenv("SHELLPIER_RELAY_URL", "")
	binary := terminalBuildTestBinary(t)
	dir := t.TempDir()
	ticketPath := filepath.Join(dir, "access.json")
	hostState, hostDone := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "30s", "--output", ticketPath, "--json", "--work-dir", dir, "--shell", "sh")
	defer func() {
		control(context.Background(), "stop", []string{"--state", hostState}, io.Discard, io.Discard)
		awaitHost(t, hostDone, hostState)
	}()
	cwd := filepath.Join(dir, "working")
	os.Mkdir(cwd, 0700)
	outputDir := filepath.Join(dir, "terminal")
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, binary, append([]string{"terminal"}, args...)...).CombinedOutput()
	}
	data, err := run("open", "@"+ticketPath, "--out", outputDir, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatalf("native PTY open %s %v", data, err)
	}
	var meta terminalMetadata
	if json.Unmarshal(data, &meta) != nil {
		t.Fatal("bad terminal metadata")
	}
	defer terminalControl(context.Background(), "close", []string{"--state", meta.StatePath}, strings.NewReader(""), io.Discard, io.Discard)
	if data, err = run("resize", "--state", meta.StatePath, "--cols", "91", "--rows", "37", "--json"); err != nil {
		t.Fatalf("native resize %s %v", data, err)
	}
	// The marker is octal-encoded in input so a terminal echo cannot pass this assertion.
	command := "printf '\\101\\103\\124\\125\\101\\114\\n'; stty size; pwd\n"
	if data, err = run("write", "--state", meta.StatePath, "--text", command, "--json"); err != nil {
		t.Fatalf("native write %s %v", data, err)
	}
	var cursor uint64
	var combined bytes.Buffer
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err = run("read", "--state", meta.StatePath, "--cursor", strconv.FormatUint(cursor, 10), "--wait", "500ms")
		if err != nil {
			t.Fatalf("native read %s %v", data, err)
		}
		var read terminalReadResult
		if json.Unmarshal(data, &read) != nil {
			t.Fatal("bad native read")
		}
		combined.Write(read.Data)
		cursor = read.NextCursor
		if strings.Contains(combined.String(), "ACTUAL") && strings.Contains(combined.String(), "37 91") && strings.Contains(combined.String(), cwd) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native terminal behavior missing: %q", combined.String())
		}
	}
	if data, err = run("write", "--state", meta.StatePath, "--text", "exit 7\n", "--json"); err != nil {
		t.Fatalf("native exit input %s %v", data, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		snapshot, e := terminalSnapshotFile(filepath.Join(outputDir, "snapshot.json"))
		if e == nil {
			if snapshot.ExitCode == nil || *snapshot.ExitCode != 7 {
				t.Fatalf("native PTY exit status %v", snapshot.ExitCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native PTY did not close: %v", e)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTerminalEscapedCommandStateBound(t *testing.T) {
	state, path := terminalTestState(t)
	state.Command = strings.Repeat("\x01", 64*1024)
	if err := terminalReplacePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	if got, err := terminalReadState(path, true); err != nil || got.Command != state.Command {
		t.Fatalf("valid 64KiB escaped command rejected: %v", err)
	}
	state.Command += "x"
	if err := terminalReplacePrivate(path, state); err != nil {
		t.Fatal(err)
	}
	if _, err := terminalReadState(path, true); err == nil {
		t.Fatal("oversized stored command accepted")
	}
}

func TestTerminalLargeEscapedSetupConfiguration(t *testing.T) {
	ticket, starts := terminalFakeSSHMode(t, true)
	state, path := terminalTestState(t)
	state.Command = strings.Repeat("\x01", 64*1024)
	state.ExpiresAt = ticket.ExpiresAt
	terminalReplacePrivate(path, state)
	env := make([]string, 4)
	for i := range env {
		env[i] = fmt.Sprintf("X%d=%s", i, strings.Repeat("\x01", 4092))
	}
	config := terminalConfig{Ticket: ticket, Retry: access.DefaultRetryOptions(), Env: env}
	configPath := filepath.Join(filepath.Dir(path), "config.json")
	if err := writeJSONPrivate(configPath, config); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(configPath)
	if info.Size() <= 65536 || info.Size() > terminalFileLimit {
		t.Fatalf("test did not exercise bounded large config: %d", info.Size())
	}
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	defer outputReader.Close()
	defer outputWriter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer outputWriter.Close()
		done <- terminalServeEntry(ctx, []string{"--state", path}, inputReader, outputWriter, io.Discard)
	}()
	io.WriteString(inputWriter, state.Secret+"\n")
	var ready struct {
		Ready bool `json:"ready"`
	}
	if err := json.NewDecoder(outputReader).Decode(&ready); err != nil || !ready.Ready {
		t.Fatalf("large legal setup rejected: %v", err)
	}
	io.WriteString(inputWriter, "commit\n")
	inputWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("large setup did not finish")
	}
	if starts.Load() != 1 {
		t.Fatal("large command replayed")
	}
}

func TestTerminalFileIdentityRemainsFrozenAfterReplacement(t *testing.T) {
	_, path := terminalTestState(t)
	original, err := terminalFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement.json")
	if err = writePrivate(replacement, []byte(`{"unrelated":"keep"}`)); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	current, err := terminalFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(original, current) {
		t.Fatal("captured identity followed replacement path")
	}
}
func TestTerminalWindowsStateUpdateWaitsForReader(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows delete-sharing behavior")
	}
	state, path := terminalTestState(t)
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	probe := filepath.Join(filepath.Dir(path), "probe.json")
	if err = writeJSONPrivate(probe, state); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(probe, path); err == nil {
		t.Fatal("fixture reader did not prevent replacement")
	} else if !terminalRetryReplace(err) {
		t.Fatalf("unexpected native rename denial: %v", err)
	}
	state.Cols = 91
	state.Rows = 37
	done := make(chan error, 1)
	go func() { done <- terminalReplacePrivate(path, state) }()
	select {
	case err = <-done:
		t.Fatalf("update completed while destination reader denied delete: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	reader.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded update failed to recover after reader released")
	}
	updated, err := terminalReadState(path, true)
	if err != nil || updated.Cols != 91 || updated.Rows != 37 {
		t.Fatalf("state update lost or invalid: %v", err)
	}
}

func TestTerminalStateRereadRequiresObservedReplacement(t *testing.T) {
	state, path := terminalTestState(t)
	calls := 0
	data, err := terminalReadStateBytes(path, func(path string, limit int64) ([]byte, error) {
		calls++
		if calls == 1 {
			state.Cols = 91
			if err := terminalReplacePrivate(path, state); err != nil {
				return nil, err
			}
			return nil, access.ErrFileChanged
		}
		return readRegular(path, limit)
	})
	var updated terminalState
	if err != nil || json.Unmarshal(data, &updated) != nil || updated.Cols != 91 || calls != 2 {
		t.Fatalf("observed update was not reread: %v calls=%d", err, calls)
	}
	calls = 0
	_, err = terminalReadStateBytes(path, func(string, int64) ([]byte, error) { calls++; return nil, access.ErrFileChanged })
	if err == nil || calls != 1 {
		t.Fatal("unobserved replacement was masked")
	}
	calls = 0
	_, err = terminalReadStateBytes(path, func(path string, _ int64) ([]byte, error) {
		calls++
		state.Cols++
		if err := terminalReplacePrivate(path, state); err != nil {
			return nil, err
		}
		return nil, access.ErrFileChanged
	})
	if err == nil || calls != 8 {
		t.Fatalf("reread not bounded: %v calls=%d", err, calls)
	}
}
func TestTerminalStateRereadRejectsNonPrivateReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode permissions; Windows DACL protection is checked by shared private-file tests")
	}
	_, path := terminalTestState(t)
	calls := 0
	_, err := terminalReadStateBytes(path, func(path string, _ int64) ([]byte, error) {
		calls++
		replacement := filepath.Join(filepath.Dir(path), "public.json")
		if err := os.WriteFile(replacement, []byte(`{"unrelated":"not private"}`), 0644); err != nil {
			return nil, err
		}
		if err := os.Rename(replacement, path); err != nil {
			return nil, err
		}
		return nil, access.ErrFileChanged
	})
	if err == nil || calls != 1 {
		t.Fatal("unsafe replacement retried or read")
	}
}
