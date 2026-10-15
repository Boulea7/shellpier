package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type terminalSlowLog struct {
	release chan struct{}
	closed  chan struct{}
}

func (w *terminalSlowLog) Write(p []byte) (int, error) { <-w.release; return len(p), nil }
func (w *terminalSlowLog) Close() error                { close(w.closed); return nil }
func TestTerminalLogBoundedAndPrivate(t *testing.T) {
	_, path := terminalTestState(t)
	log, err := newTerminalLog(filepath.Dir(path), 5)
	if err != nil {
		t.Fatal(err)
	}
	log.enqueue([]byte("123456789"))
	log.stop()
	status := log.snapshot()
	if status.BytesWritten != 5 || !status.Truncated || !status.Complete {
		t.Fatalf("bad bounded log %#v", status)
	}
	logPath := filepath.Join(filepath.Dir(path), "output.log")
	if err := checkPrivateSessionFile(logPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || string(data) != "12345" {
		t.Fatal(string(data), err)
	}
	if _, err := newTerminalLog(filepath.Dir(path), 5); err == nil {
		t.Fatal("existing log overwritten")
	}
	if _, err := newTerminalLog(filepath.Dir(path), terminalLogMax+1); err == nil {
		t.Fatal("excess limit accepted")
	}
}
func TestTerminalLogSlowWriterDoesNotBlockRing(t *testing.T) {
	writer := &terminalSlowLog{release: make(chan struct{}), closed: make(chan struct{})}
	log := startTerminalLog(writer, terminalLogMax)
	ring := newTerminalRing()
	ring.log = log
	done := make(chan struct{})
	go func() { ring.Write(bytes.Repeat([]byte("x"), terminalCapacity)); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(writer.release)
		t.Fatal("slow log blocked SSH output")
	}
	if status := log.snapshot(); !status.Truncated || status.Error == "" {
		t.Fatal("queue saturation not reported")
	}
	if ring.snapshot().NextCursor != terminalCapacity {
		t.Fatal("log loss changed terminal output")
	}
	close(writer.release)
	log.stop()
	select {
	case <-writer.closed:
	default:
		t.Fatal("writer not closed")
	}
}

type terminalBrokenLog struct{}

func (terminalBrokenLog) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (terminalBrokenLog) Close() error              { return nil }
func TestTerminalLogWriteErrorRecorded(t *testing.T) {
	log := startTerminalLog(terminalBrokenLog{}, 100)
	log.enqueue([]byte("x"))
	log.stop()
	status := log.snapshot()
	if status.Error == "" || !status.Truncated || status.Complete {
		t.Fatalf("write error hidden %#v", status)
	}
}
