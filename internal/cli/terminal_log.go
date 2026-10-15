package cli

import (
	"errors"
	"github.com/Boulea7/shellpier/internal/platform"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const terminalLogMax = 64 << 20
const terminalLogChunk = 32 << 10
const terminalLogQueue = 16

type terminalLogStatus struct {
	Limit        int64  `json:"limit"`
	BytesWritten int64  `json:"bytes_written"`
	Truncated    bool   `json:"truncated"`
	Error        string `json:"error,omitempty"`
	Complete     bool   `json:"complete"`
}
type terminalLog struct {
	mu       sync.Mutex
	status   terminalLogStatus
	accepted int64
	stopped  bool
	queue    chan []byte
	done     chan struct{}
}

// Output logs are optional, bounded, private files in the new session directory.
func newTerminalLog(directory string, limit int64) (*terminalLog, error) {
	if limit < 1 || limit > terminalLogMax {
		return nil, errors.New("log limit must be 1..67108864")
	}
	if err := checkPrivateSessionDir(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "output.log")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if err = platform.SecureFile(path); err != nil {
		file.Close()
		return nil, err
	}
	current, err := terminalFileIdentity(path)
	if err != nil || !os.SameFile(opened, current) {
		file.Close()
		return nil, errors.New("log file changed while securing")
	}
	if err := checkPrivateSessionFile(path); err != nil {
		file.Close()
		return nil, err
	}
	return startTerminalLog(file, limit), nil
}
func startTerminalLog(writer io.WriteCloser, limit int64) *terminalLog {
	log := &terminalLog{status: terminalLogStatus{Limit: limit}, queue: make(chan []byte, terminalLogQueue), done: make(chan struct{})}
	go func() {
		defer close(log.done)
		failed := false
		for data := range log.queue {
			if failed {
				continue
			}
			n, err := writer.Write(data)
			log.mu.Lock()
			log.status.BytesWritten += int64(n)
			if err != nil || n != len(data) {
				log.status.Error = "log write failed"
				log.status.Truncated = true
				failed = true
			}
			log.mu.Unlock()
		}
		err := writer.Close()
		log.mu.Lock()
		if err != nil {
			log.status.Error = "log close failed"
		}
		log.status.Complete = err == nil && !failed
		log.mu.Unlock()
	}()
	return log
}

// enqueue never waits on filesystem I/O; saturation discards log bytes only.
func (log *terminalLog) enqueue(data []byte) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.stopped {
		return
	}
	for len(data) > 0 {
		remaining := log.status.Limit - log.accepted
		if remaining <= 0 {
			log.status.Truncated = true
			return
		}
		n := len(data)
		if n > terminalLogChunk {
			n = terminalLogChunk
		}
		if int64(n) > remaining {
			n = int(remaining)
		}
		chunk := append([]byte(nil), data[:n]...)
		select {
		case log.queue <- chunk:
			log.accepted += int64(n)
		default:
			log.status.Truncated = true
			log.status.Error = "log queue full; output records dropped"
			return
		}
		data = data[n:]
	}
}
func (log *terminalLog) stop() {
	log.mu.Lock()
	if !log.stopped {
		log.stopped = true
		close(log.queue)
	}
	log.mu.Unlock()
	select {
	case <-log.done:
	case <-time.After(time.Second):
		log.mu.Lock()
		log.status.Truncated = true
		log.status.Error = "log flush timed out"
		log.mu.Unlock()
	}
}
func (log *terminalLog) snapshot() *terminalLogStatus {
	log.mu.Lock()
	defer log.mu.Unlock()
	status := log.status
	return &status
}
