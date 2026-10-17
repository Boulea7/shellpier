// Package terminals owns bounded PTYs for the lifetime of one host process.
package terminals

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
)

const MaxPayload = 96 << 10
const MaxRead = 32 << 10
const MaxWrite = 32 << 10
const MaxTerminals = 32
const MaxRunning = 4
const MaxOperations = 256
const QueueCapacity = 8
const LogBytes = 1 << 20

type Options struct{ Shell, WorkDir string }
type Request struct {
	Version     int      `json:"version"`
	Action      string   `json:"action"`
	ID          string   `json:"id,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
	Command     string   `json:"command,omitempty"`
	CWD         string   `json:"cwd,omitempty"`
	Env         []string `json:"env,omitempty"`
	TimeoutMS   int64    `json:"timeout_ms,omitempty"`
	Term        string   `json:"term,omitempty"`
	Cols        int      `json:"cols,omitempty"`
	Rows        int      `json:"rows,omitempty"`
	Cursor      uint64   `json:"cursor,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Data        []byte   `json:"data,omitempty"`
	Signal      string   `json:"signal,omitempty"`
}
type Terminal struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operation_id"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Cols        int       `json:"cols"`
	Rows        int       `json:"rows"`
	NextCursor  uint64    `json:"next_cursor"`
	ExitCode    *uint32   `json:"exit_code,omitempty"`
	Error       string    `json:"error,omitempty"`
}
type Operation struct {
	OperationID    string    `json:"operation_id"`
	TerminalID     string    `json:"terminal_id"`
	Status         string    `json:"status"`
	BytesWritten   int       `json:"bytes_written"`
	Error          string    `json:"error,omitempty"`
	DedupScope     string    `json:"dedup_scope"`
	DedupExpiresAt time.Time `json:"dedup_expires_at"`
}
type Chunk struct {
	Data       []byte `json:"data"`
	NextCursor uint64 `json:"next_cursor"`
	Dropped    uint64 `json:"dropped"`
}
type Response struct {
	OK        bool       `json:"ok"`
	Error     string     `json:"error,omitempty"`
	Terminal  *Terminal  `json:"terminal,omitempty"`
	Terminals []Terminal `json:"terminals,omitempty"`
	Operation *Operation `json:"operation,omitempty"`
	Output    *Chunk     `json:"output,omitempty"`
}
type operation struct {
	Operation
	digest [32]byte
}
type writeRequest struct {
	operation *operation
	data      []byte
}
type entry struct {
	Terminal
	ctx      context.Context
	cancel   context.CancelFunc
	terminal platform.Terminal
	open     *operation
	writes   map[string]*operation
	queue    chan writeRequest
	output   []byte
	closing  bool
}
type Manager struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	options Options
	entries map[string]*entry
	opens   map[string]*entry
	running int
	closed  bool
	workers sync.WaitGroup
	start   func(context.Context, platform.TerminalOptions) (platform.Terminal, error)
}

func New(ctx context.Context, options Options) (*Manager, error) {
	if _, err := platform.Command(options.Shell, "", options.WorkDir); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{ctx: ctx, cancel: cancel, options: options, entries: map[string]*entry{}, opens: map[string]*entry{}, start: platform.StartTerminal}, nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("terminal cancellation did not complete within 5s")
	}
}
func ValidateOperationID(id string) error {
	if len(id) < 1 || len(id) > 64 {
		return errors.New("operation ID must contain 1..64 ASCII letters, digits, dot, underscore, or hyphen")
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return errors.New("invalid operation ID")
		}
	}
	return nil
}
func (m *Manager) Handle(payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > MaxPayload {
		return nil, errors.New("terminal request exceeds payload limit")
	}
	var r Request
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return nil, errors.New("invalid terminal request")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing terminal request data")
	}
	return json.Marshal(m.Do(r))
}
func fail(s string) Response { return Response{Error: s} }
func (m *Manager) response(e *entry, op *operation) Response {
	t := e.Terminal
	if t.ExitCode != nil {
		n := *t.ExitCode
		t.ExitCode = &n
	}
	r := Response{OK: true, Terminal: &t}
	if op != nil {
		o := op.Operation
		r.Operation = &o
	}
	return r
}
func (m *Manager) Do(r Request) Response {
	if r.Version != 1 {
		return fail("unsupported terminal protocol version")
	}
	if r.Action == "open" {
		return m.open(r)
	}
	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		return fail("terminal manager is closed")
	}
	if r.Action == "list" {
		values := make([]Terminal, 0, len(m.entries))
		for _, e := range m.entries {
			values = append(values, *m.response(e, nil).Terminal)
		}
		sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
		m.mu.Unlock()
		return Response{OK: true, Terminals: values}
	}
	if r.Action == "operation" && r.ID == "" {
		e := m.opens[r.OperationID]
		if e == nil {
			m.mu.Unlock()
			return fail("open operation not found")
		}
		out := m.response(e, e.open)
		m.mu.Unlock()
		return out
	}
	e := m.entries[r.ID]
	if e == nil {
		m.mu.Unlock()
		return fail("terminal not found")
	}
	out := m.response(e, nil)
	switch r.Action {
	case "status":
		m.mu.Unlock()
		return out
	case "operation":
		op := e.writes[r.OperationID]
		if op == nil {
			m.mu.Unlock()
			return fail("write operation not found")
		}
		out = m.response(e, op)
		m.mu.Unlock()
		return out
	case "read":
		limit := r.Limit
		if limit == 0 {
			limit = MaxRead
		}
		if limit < 1 || limit > MaxRead || r.Cursor > e.NextCursor {
			m.mu.Unlock()
			return fail("invalid terminal cursor or read limit")
		}
		base := e.NextCursor - uint64(len(e.output))
		cursor := r.Cursor
		dropped := uint64(0)
		if cursor < base {
			dropped = base - cursor
			cursor = base
		}
		n := min(uint64(limit), e.NextCursor-cursor)
		out.Output = &Chunk{Data: append([]byte{}, e.output[cursor-base:cursor-base+n]...), NextCursor: cursor + n, Dropped: dropped}
		m.mu.Unlock()
		return out
	case "close":
		e.closing = true
		e.cancel()
		if e.State == "pending" || e.State == "running" {
			e.State = "closing"
		}
		out = m.response(e, nil)
		m.mu.Unlock()
		return out
	case "write":
		if ValidateOperationID(r.OperationID) != nil || len(r.Data) == 0 || len(r.Data) > MaxWrite {
			m.mu.Unlock()
			return fail("invalid write operation ID or data size")
		}
		digest := sha256.Sum256(r.Data)
		if op := e.writes[r.OperationID]; op != nil {
			if op.digest != digest {
				m.mu.Unlock()
				return fail("operation ID content conflict")
			}
			out = m.response(e, op)
			m.mu.Unlock()
			return out
		}
		if e.State != "running" || e.closing {
			m.mu.Unlock()
			return fail("terminal is not running")
		}
		if len(e.writes) >= MaxOperations {
			m.mu.Unlock()
			return fail("write operation table capacity reached")
		}
		op := &operation{Operation: Operation{OperationID: r.OperationID, TerminalID: e.ID, Status: "pending", DedupScope: "host_lifetime", DedupExpiresAt: e.ExpiresAt}, digest: digest}
		select {
		case e.queue <- writeRequest{op, append([]byte(nil), r.Data...)}:
			e.writes[r.OperationID] = op
			out = m.response(e, op)
			m.mu.Unlock()
			return out
		default:
			m.mu.Unlock()
			return fail("terminal write queue is full")
		}
	case "resize", "signal":
		if e.State != "running" || e.closing {
			m.mu.Unlock()
			return fail("terminal is not running")
		}
		p := e.terminal
		m.mu.Unlock()
		var err error
		if r.Action == "resize" {
			if r.Cols < 1 || r.Cols > 1000 || r.Rows < 1 || r.Rows > 1000 {
				return fail("terminal dimensions must be 1..1000")
			}
			err = p.Resize(uint32(r.Cols), uint32(r.Rows))
		} else {
			allowed := false
			for _, s := range platform.TerminalSignals() {
				if r.Signal == s {
					allowed = true
				}
			}
			if !allowed {
				return fail("unsupported terminal signal")
			}
			err = p.Signal(r.Signal)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			return fail("terminal control failed")
		}
		if r.Action == "resize" {
			e.Cols, e.Rows = r.Cols, r.Rows
		}
		return m.response(e, nil)
	default:
		m.mu.Unlock()
		return fail("unsupported terminal action")
	}
}
func (m *Manager) open(r Request) Response {
	if ValidateOperationID(r.OperationID) != nil || len(r.Command) > 64<<10 || strings.ContainsRune(r.Command, 0) || len(r.CWD) > 4096 || strings.ContainsRune(r.CWD, 0) || len(r.Term) > 128 || strings.ContainsAny(r.Term, "\x00\r\n") {
		return fail("invalid terminal open options")
	}
	if r.Cols == 0 {
		r.Cols = 80
	}
	if r.Rows == 0 {
		r.Rows = 24
	}
	if r.Term == "" {
		r.Term = "xterm"
	}
	if r.TimeoutMS == 0 {
		r.TimeoutMS = (3 * time.Hour).Milliseconds()
	}
	if r.Cols < 1 || r.Cols > 1000 || r.Rows < 1 || r.Rows > 1000 || r.TimeoutMS < 1 || r.TimeoutMS > (24*time.Hour).Milliseconds() {
		return fail("invalid terminal dimensions or lifetime")
	}
	if len(r.Env) > 32 {
		return fail("too many terminal environment entries")
	}
	total := 0
	seen := map[string]bool{}
	for _, s := range r.Env {
		key, value, ok := strings.Cut(s, "=")
		if !ok || len(key) == 0 || len(key) > 64 || len(value) > 4096 || strings.ContainsRune(s, 0) {
			return fail("invalid terminal environment")
		}
		for i, c := range key {
			if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
				return fail("invalid terminal environment name")
			}
		}
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if seen[key] {
			return fail("duplicate terminal environment name")
		}
		seen[key] = true
		total += len(s)
	}
	if total > 16<<10 {
		return fail("terminal environment exceeds limit")
	}
	canonical := Request{Action: "open", OperationID: r.OperationID, Command: r.Command, CWD: r.CWD, Env: r.Env, TimeoutMS: r.TimeoutMS, Term: r.Term, Cols: r.Cols, Rows: r.Rows}
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return fail("terminal manager is closed")
	}
	if e := m.opens[r.OperationID]; e != nil {
		if e.open.digest != digest {
			return fail("operation ID content conflict")
		}
		return m.response(e, e.open)
	}
	if len(m.entries) >= MaxTerminals || m.running >= MaxRunning {
		return fail("terminal table or running capacity reached")
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return fail("cannot generate terminal ID")
	}
	id := hex.EncodeToString(token)
	ctx, cancel := context.WithTimeout(m.ctx, time.Duration(r.TimeoutMS)*time.Millisecond)
	expiry, _ := ctx.Deadline()
	e := &entry{Terminal: Terminal{ID: id, OperationID: r.OperationID, State: "pending", CreatedAt: time.Now().UTC(), ExpiresAt: expiry.UTC(), Cols: r.Cols, Rows: r.Rows}, ctx: ctx, cancel: cancel, writes: map[string]*operation{}, queue: make(chan writeRequest, QueueCapacity)}
	e.open = &operation{Operation: Operation{OperationID: r.OperationID, TerminalID: id, Status: "pending", DedupScope: "host_lifetime", DedupExpiresAt: e.ExpiresAt}, digest: digest}
	m.entries[id] = e
	m.opens[r.OperationID] = e
	m.running++
	m.workers.Add(1)
	go m.run(e, r)
	return m.response(e, e.open)
}
func (m *Manager) run(e *entry, r Request) {
	defer m.workers.Done()
	defer e.cancel()
	dir := r.CWD
	if dir == "" {
		dir = m.options.WorkDir
	} else if !filepath.IsAbs(dir) {
		if filepath.VolumeName(dir) != "" {
			m.finishStart(e)
			return
		}
		dir = filepath.Join(m.options.WorkDir, dir)
	}
	if dir != "" {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			m.finishStart(e)
			return
		}
	}
	m.mu.Lock()
	e.open.Status = "running"
	m.mu.Unlock()
	p, err := m.start(e.ctx, platform.TerminalOptions{Shell: m.options.Shell, Command: r.Command, WorkDir: dir, Term: r.Term, Columns: uint32(r.Cols), Rows: uint32(r.Rows), Env: r.Env})
	if err != nil {
		m.finishStart(e)
		return
	}
	m.mu.Lock()
	e.terminal = p
	e.open.Status = "completed"
	if !e.closing {
		e.State = "running"
	}
	m.mu.Unlock()
	go func() { <-e.ctx.Done(); _ = p.Close() }()
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		buffer := make([]byte, 32768)
		for {
			n, err := p.Read(buffer)
			if n > 0 {
				m.mu.Lock()
				e.NextCursor += uint64(n)
				if n >= LogBytes {
					e.output = append(e.output[:0], buffer[n-LogBytes:n]...)
				} else {
					excess := len(e.output) + n - LogBytes
					if excess > 0 {
						copy(e.output, e.output[excess:])
						e.output = e.output[:len(e.output)-excess]
					}
					e.output = append(e.output, buffer[:n]...)
				}
				m.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-e.ctx.Done():
				return
			case w := <-e.queue:
				m.mu.Lock()
				if e.ctx.Err() != nil {
					w.operation.Status = "failed"
					w.operation.Error = "terminal closed before write"
					m.mu.Unlock()
					continue
				}
				w.operation.Status = "running"
				m.mu.Unlock()
				n, err := p.Write(w.data)
				m.mu.Lock()
				w.operation.BytesWritten = n
				if err != nil || n != len(w.data) {
					w.operation.Status = "failed"
					w.operation.Error = "terminal write failed or was partial"
				} else {
					w.operation.Status = "completed"
				}
				m.mu.Unlock()
			}
		}
	}()
	code, waitErr := p.Wait()
	select {
	case <-drain:
	case <-time.After(200 * time.Millisecond):
	}
	_ = p.Close()
	<-drain
	e.cancel()
	<-writeDone
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	e.ExitCode = &code
	e.FinishedAt = time.Now().UTC()
	e.State = "completed"
	if waitErr != nil {
		e.State = "failed"
		e.Error = "terminal wait failed"
	}
	if e.ctx.Err() == context.DeadlineExceeded {
		e.State = "timed_out"
	} else if e.closing || m.ctx.Err() != nil {
		e.State = "closed"
	}
	for _, op := range e.writes {
		if op.Status == "pending" || op.Status == "running" {
			op.Status = "failed"
			op.Error = "terminal closed before write completed"
		}
	}
}
func (m *Manager) finishStart(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	e.State = "failed"
	e.Error = "terminal could not start"
	e.FinishedAt = time.Now().UTC()
	e.open.Status = "failed"
	e.open.Error = e.Error
}
