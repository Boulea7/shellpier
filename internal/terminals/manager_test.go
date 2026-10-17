package terminals

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "cmd"
	}
	m, err := New(context.Background(), Options{Shell: shell, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}
func rpc(m *Manager, r Request) Response { r.Version = 1; return m.Do(r) }
func await(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatal("condition did not complete")
}
func opened(t *testing.T, m *Manager, op string) *Terminal {
	t.Helper()
	r := rpc(m, Request{Action: "open", OperationID: op})
	if !r.OK {
		t.Fatal(r.Error)
	}
	id := r.Terminal.ID
	await(t, func() bool {
		r = rpc(m, Request{Action: "status", ID: id})
		return r.Terminal.State == "running" || r.Terminal.State == "failed"
	})
	if r.Terminal.State != "running" {
		t.Fatal(r)
	}
	return r.Terminal
}
func TestRealTerminalStateAndDedup(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("no PTY")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell test")
	}
	m := testManager(t)
	term := opened(t, m, "open-once")
	again := rpc(m, Request{Action: "open", OperationID: "open-once"})
	if again.Terminal.ID != term.ID {
		t.Fatal("duplicate PTY")
	}
	if rpc(m, Request{Action: "open", OperationID: "open-once", Command: "true"}).OK {
		t.Fatal("accepted conflict")
	}
	data := []byte("SHELLPIER_VALUE=retained; cd /; printf '__STATE_%s_%s__\\n' \"$SHELLPIER_VALUE\" \"$PWD\"\n")
	w := rpc(m, Request{Action: "write", ID: term.ID, OperationID: "write-once", Data: data})
	if !w.OK {
		t.Fatal(w)
	}
	await(t, func() bool {
		return rpc(m, Request{Action: "operation", ID: term.ID, OperationID: "write-once"}).Operation.Status == "completed"
	})
	again = rpc(m, Request{Action: "write", ID: term.ID, OperationID: "write-once", Data: data})
	if again.Operation.Status != "completed" {
		t.Fatal(again)
	}
	if rpc(m, Request{Action: "write", ID: term.ID, OperationID: "write-once", Data: []byte("different")}).OK {
		t.Fatal("accepted write conflict")
	}
	await(t, func() bool {
		return bytes.Contains(rpc(m, Request{Action: "read", ID: term.ID}).Output.Data, []byte("__STATE_retained_/__"))
	})
	rpc(m, Request{Action: "write", ID: term.ID, OperationID: "second", Data: []byte("printf '__NEXT_%s__\\n' \"$SHELLPIER_VALUE\"\n")})
	await(t, func() bool {
		return bytes.Contains(rpc(m, Request{Action: "read", ID: term.ID}).Output.Data, []byte("__NEXT_retained__"))
	})
}

type fakeTerminal struct {
	reader       *io.PipeReader
	writer       *io.PipeWriter
	done         chan struct{}
	once         sync.Once
	writeEntered chan struct{}
	writes       int
	mu           sync.Mutex
}

func newFake() *fakeTerminal {
	r, w := io.Pipe()
	return &fakeTerminal{reader: r, writer: w, done: make(chan struct{}), writeEntered: make(chan struct{}, 1)}
}
func (f *fakeTerminal) Read(p []byte) (int, error) { return f.reader.Read(p) }
func (f *fakeTerminal) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	select {
	case f.writeEntered <- struct{}{}:
	default:
	}
	<-f.done
	return 0, io.ErrClosedPipe
}
func (f *fakeTerminal) Resize(uint32, uint32) error { return nil }
func (f *fakeTerminal) Signal(string) error         { return nil }
func (f *fakeTerminal) Wait() (uint32, error)       { <-f.done; return 0, nil }
func (f *fakeTerminal) Close() error {
	f.once.Do(func() { close(f.done); f.writer.Close(); f.reader.Close() })
	return nil
}
func fakeManager(t *testing.T) (*Manager, *fakeTerminal) {
	m := testManager(t)
	f := newFake()
	m.start = func(context.Context, platform.TerminalOptions) (platform.Terminal, error) { return f, nil }
	return m, f
}
func TestBlockedWriteQueueAndClose(t *testing.T) {
	m, f := fakeManager(t)
	term := opened(t, m, "open")
	if !rpc(m, Request{Action: "write", ID: term.ID, OperationID: "first", Data: []byte("x")}).OK {
		t.Fatal("write failed")
	}
	<-f.writeEntered
	for i := 0; i < QueueCapacity; i++ {
		if !rpc(m, Request{Action: "write", ID: term.ID, OperationID: string(rune('a' + i)), Data: []byte("x")}).OK {
			t.Fatal("queue unexpectedly full")
		}
	}
	if rpc(m, Request{Action: "write", ID: term.ID, OperationID: "overflow", Data: []byte("x")}).OK {
		t.Fatal("queue not bounded")
	}
	if !rpc(m, Request{Action: "status", ID: term.ID}).OK || !rpc(m, Request{Action: "read", ID: term.ID}).OK {
		t.Fatal("blocked control")
	}
	rpc(m, Request{Action: "close", ID: term.ID})
	await(t, func() bool { return rpc(m, Request{Action: "status", ID: term.ID}).Terminal.State == "closed" })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 {
		t.Fatalf("replayed pending writes: %d", f.writes)
	}
}
func TestRingCursorAndProtocolBounds(t *testing.T) {
	m, f := fakeManager(t)
	term := opened(t, m, "open")
	data := bytes.Repeat([]byte("z"), LogBytes+100)
	done := make(chan struct{})
	go func() { f.writer.Write(data); close(done) }()
	<-done
	await(t, func() bool {
		return rpc(m, Request{Action: "status", ID: term.ID}).Terminal.NextCursor == uint64(len(data))
	})
	r := rpc(m, Request{Action: "read", ID: term.ID, Limit: 10})
	if r.Output.Dropped != 100 || r.Output.NextCursor != 110 || string(r.Output.Data) != strings.Repeat("z", 10) {
		t.Fatal(r.Output)
	}
	if rpc(m, Request{Action: "read", ID: term.ID, Cursor: uint64(len(data) + 1)}).OK {
		t.Fatal("ahead cursor accepted")
	}
	if m.Do(Request{Action: "list"}).OK {
		t.Fatal("missing version accepted")
	}
	if _, err := m.Handle([]byte(`{"version":1,"action":"list","unknown":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestHostCancellationAndOperationCapacity(t *testing.T) {
	m, f := fakeManager(t)
	term := opened(t, m, "open")
	m.mu.Lock()
	e := m.entries[term.ID]
	for i := 0; i < MaxOperations; i++ {
		e.writes[string(rune(i+1))] = &operation{}
	}
	m.mu.Unlock()
	if rpc(m, Request{Action: "write", ID: term.ID, OperationID: "full", Data: []byte("x")}).OK {
		t.Fatal("operation capacity not bounded")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.done:
	default:
		t.Fatal("host did not close PTY")
	}
}

type acceptingTerminal struct{ *fakeTerminal }

func (f *acceptingTerminal) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return len(p), nil
}
func TestWriteOperationsRetainedAtCapacity(t *testing.T) {
	m := testManager(t)
	f := &acceptingTerminal{newFake()}
	m.start = func(context.Context, platform.TerminalOptions) (platform.Terminal, error) { return f, nil }
	term := opened(t, m, "open")
	for i := 0; i < MaxOperations; i++ {
		op := fmt.Sprintf("write-%d", i)
		r := rpc(m, Request{Action: "write", ID: term.ID, OperationID: op, Data: []byte("x")})
		if !r.OK {
			t.Fatal(r.Error)
		}
		await(t, func() bool {
			return rpc(m, Request{Action: "operation", ID: term.ID, OperationID: op}).Operation.Status == "completed"
		})
	}
	if rpc(m, Request{Action: "write", ID: term.ID, OperationID: "extra", Data: []byte("x")}).OK {
		t.Fatal("operation capacity exceeded")
	}
	r := rpc(m, Request{Action: "write", ID: term.ID, OperationID: "write-0", Data: []byte("x")})
	if !r.OK || r.Operation.Status != "completed" {
		t.Fatal("lost committed operation")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != MaxOperations {
		t.Fatal("replayed write")
	}
}
func TestTerminalTTL(t *testing.T) {
	m, f := fakeManager(t)
	r := rpc(m, Request{Action: "open", OperationID: "expiry", TimeoutMS: 50})
	if !r.OK {
		t.Fatal(r)
	}
	await(t, func() bool { return rpc(m, Request{Action: "status", ID: r.Terminal.ID}).Terminal.State == "timed_out" })
	select {
	case <-f.done:
	default:
		t.Fatal("expiry did not close PTY")
	}
}
func TestOpenOperationsRetainedAndBounded(t *testing.T) {
	m := testManager(t)
	m.start = func(context.Context, platform.TerminalOptions) (platform.Terminal, error) {
		return nil, errors.New("start failed")
	}
	for i := 0; i < MaxTerminals; i++ {
		op := fmt.Sprintf("open-%d", i)
		r := rpc(m, Request{Action: "open", OperationID: op})
		if !r.OK {
			t.Fatal(r)
		}
		await(t, func() bool { return rpc(m, Request{Action: "operation", OperationID: op}).Operation.Status == "failed" })
	}
	if rpc(m, Request{Action: "open", OperationID: "overflow"}).OK {
		t.Fatal("open table capacity exceeded")
	}
	if r := rpc(m, Request{Action: "open", OperationID: "open-0"}); !r.OK || r.Operation.Status != "failed" {
		t.Fatal("open record lost")
	}
}
func TestRealHostCloseStopsChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" || !platform.TerminalSupported() {
		t.Skip("Unix process probe")
	}
	m := testManager(t)
	r := rpc(m, Request{Action: "open", OperationID: "tree", Command: "sleep 60 & printf '__CHILD_%s__\\n' \"$!\"; wait"})
	if !r.OK {
		t.Fatal(r)
	}
	var pid string
	await(t, func() bool {
		out := rpc(m, Request{Action: "read", ID: r.Terminal.ID})
		if out.Output == nil {
			return false
		}
		match := regexp.MustCompile(`__CHILD_([0-9]+)__`).FindSubmatch(out.Output.Data)
		if len(match) != 2 {
			return false
		}
		pid = string(match[1])
		return true
	})
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return exec.Command("sh", "-c", "kill -0 "+pid+" 2>/dev/null").Run() != nil })
}
