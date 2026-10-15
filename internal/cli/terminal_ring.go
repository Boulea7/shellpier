package cli

import (
	"context"
	"errors"
	"sync"
	"time"
)

const terminalCapacity = 1 << 20
const terminalDefaultRead = 64 << 10

type terminalReadResult struct {
	Log               *terminalLogStatus `json:"log,omitempty"`
	TerminationReason string             `json:"termination_reason,omitempty"`
	ExitStatusKnown   bool               `json:"exit_status_known"`
	SessionID         string             `json:"session_id,omitempty"`
	Data              []byte             `json:"data"`
	NextCursor        uint64             `json:"next_cursor"`
	Dropped           uint64             `json:"dropped"`
	Closed            bool               `json:"closed"`
	ExitCode          *int               `json:"exit_code,omitempty"`
}

type terminalRing struct {
	log      *terminalLog
	mu       sync.Mutex
	data     []byte
	offset   uint64
	closed   bool
	exitCode *int
	changed  chan struct{}
	reason   string
	known    bool
}

func newTerminalRing() *terminalRing { return &terminalRing{changed: make(chan struct{})} }
func (r *terminalRing) notify()      { close(r.changed); r.changed = make(chan struct{}) }
func (r *terminalRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errors.New("terminal output is closed")
	}
	if r.log != nil {
		r.log.enqueue(p)
	}
	n := len(p)
	r.offset += uint64(n)
	if n >= terminalCapacity {
		r.data = append(r.data[:0], p[n-terminalCapacity:]...)
	} else {
		if len(r.data)+n > terminalCapacity {
			drop := len(r.data) + n - terminalCapacity
			copy(r.data, r.data[drop:])
			r.data = r.data[:len(r.data)-drop]
		}
		r.data = append(r.data, p...)
	}
	r.notify()
	return n, nil
}
func (r *terminalRing) finish(code int) { r.finishOutcome(code, "exited", code >= 0) }
func (r *terminalRing) finishOutcome(code int, reason string, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.reason = reason
		r.known = known
		r.exitCode = &code
		r.notify()
	}
}
func (r *terminalRing) read(ctx context.Context, cursor uint64, limit int, wait time.Duration) (terminalReadResult, error) {
	if limit < 1 || limit > terminalCapacity || wait < 0 || wait > 30*time.Second {
		return terminalReadResult{}, errors.New("invalid terminal read bounds")
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		if cursor > r.offset {
			r.mu.Unlock()
			return terminalReadResult{}, errors.New("cursor is beyond terminal output")
		}
		base := r.offset - uint64(len(r.data))
		dropped := uint64(0)
		if cursor < base {
			dropped = base - cursor
			cursor = base
		}
		count := int(r.offset - cursor)
		if count > limit {
			count = limit
		}
		result := terminalReadResult{Data: append([]byte{}, r.data[int(cursor-base):int(cursor-base)+count]...), NextCursor: cursor + uint64(count), Dropped: dropped, Closed: r.closed, TerminationReason: r.reason, ExitStatusKnown: r.known}
		if r.log != nil {
			result.Log = r.log.snapshot()
		}
		if r.exitCode != nil {
			code := *r.exitCode
			result.ExitCode = &code
		}
		changed := r.changed
		r.mu.Unlock()
		if count > 0 || result.Closed || dropped > 0 || wait == 0 {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return terminalReadResult{}, ctx.Err()
		case <-deadline.C:
			return result, nil
		case <-changed:
		}
	}
}
func (r *terminalRing) snapshot() terminalReadResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := terminalReadResult{Data: append([]byte{}, r.data...), NextCursor: r.offset, Dropped: r.offset - uint64(len(r.data)), Closed: r.closed, TerminationReason: r.reason, ExitStatusKnown: r.known}
	if r.log != nil {
		result.Log = r.log.snapshot()
	}
	if r.exitCode != nil {
		code := *r.exitCode
		result.ExitCode = &code
	}
	return result
}
func terminalReadSnapshot(s terminalReadResult, cursor uint64, limit int) (terminalReadResult, error) {
	if len(s.Data) > terminalCapacity || s.NextCursor < uint64(len(s.Data)) || !s.Closed || limit < 1 || limit > terminalCapacity || cursor > s.NextCursor {
		return terminalReadResult{}, errors.New("invalid terminal snapshot or cursor")
	}
	base := s.NextCursor - uint64(len(s.Data))
	dropped := uint64(0)
	if cursor < base {
		dropped = base - cursor
		cursor = base
	}
	count := int(s.NextCursor - cursor)
	if count > limit {
		count = limit
	}
	return terminalReadResult{Log: s.Log, Data: append([]byte{}, s.Data[int(cursor-base):int(cursor-base)+count]...), NextCursor: cursor + uint64(count), Dropped: dropped, Closed: true, ExitCode: s.ExitCode, TerminationReason: s.TerminationReason, ExitStatusKnown: s.ExitStatusKnown}, nil
}
