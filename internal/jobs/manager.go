// Package jobs manages bounded tasks owned by one host session, not SSH connections.
package jobs

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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
)

const MaxPayload = 96 * 1024
const MaxRead = 32 * 1024

type Options struct {
	Shell, WorkDir                   string
	MaxJobs, MaxConcurrent, LogBytes int
	DefaultTTL, MaxTTL               time.Duration
}

func DefaultOptions() Options {
	return Options{Shell: "auto", MaxJobs: 128, MaxConcurrent: 4, LogBytes: 1 << 20, DefaultTTL: 3 * time.Hour, MaxTTL: 24 * time.Hour}
}

type Request struct {
	After        string   `json:"after,omitempty"`
	Action       string   `json:"action"`
	ID           string   `json:"id,omitempty"`
	OperationID  string   `json:"operation_id,omitempty"`
	Command      string   `json:"command,omitempty"`
	CWD          string   `json:"cwd,omitempty"`
	Env          []string `json:"env,omitempty"`
	TimeoutMS    int64    `json:"timeout_ms,omitempty"`
	StdoutCursor int64    `json:"stdout_cursor,omitempty"`
	StderrCursor int64    `json:"stderr_cursor,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}
type Job struct {
	ID          string    `json:"id"`
	OperationID string    `json:"operation_id"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	ExitCode    *uint32   `json:"exit_code,omitempty"`
	Error       string    `json:"error,omitempty"`
}
type LogChunk struct {
	Data    []byte `json:"data"`
	Cursor  int64  `json:"cursor"`
	Dropped int64  `json:"dropped"`
}
type Response struct {
	NextCursor string    `json:"next_cursor,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	Job        *Job      `json:"job,omitempty"`
	Jobs       []Job     `json:"jobs,omitempty"`
	Stdout     *LogChunk `json:"stdout,omitempty"`
	Stderr     *LogChunk `json:"stderr,omitempty"`
}
type entry struct {
	Job
	digest         [32]byte
	cancel         context.CancelFunc
	stdout, stderr ring
}
type Manager struct {
	ctx        context.Context
	cancel     context.CancelFunc
	options    Options
	mu         sync.Mutex
	entries    map[string]*entry
	operations map[string]string
	running    int
	closed     bool
	workers    sync.WaitGroup
}

func New(ctx context.Context, o Options) (*Manager, error) {
	if o.MaxJobs == 0 {
		o.MaxJobs = 128
	}
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = 4
	}
	if o.LogBytes == 0 {
		o.LogBytes = 1 << 20
	}
	if o.DefaultTTL == 0 {
		o.DefaultTTL = 3 * time.Hour
	}
	if o.MaxTTL == 0 {
		o.MaxTTL = 24 * time.Hour
	}
	if o.MaxJobs < 1 || o.MaxJobs > 1024 || o.MaxConcurrent < 1 || o.MaxConcurrent > 64 || o.MaxConcurrent > o.MaxJobs || o.LogBytes < 1024 || o.LogBytes > 16<<20 || o.DefaultTTL < time.Millisecond || o.MaxTTL < o.DefaultTTL || o.MaxTTL > 7*24*time.Hour {
		return nil, errors.New("invalid job limits")
	}
	if _, err := platform.Command(o.Shell, "", o.WorkDir); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{ctx: ctx, cancel: cancel, options: o, entries: map[string]*entry{}, operations: map[string]string{}}, nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.workers.Wait(); close(done) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("job cancellation did not complete within 5s")
	}
}
func (m *Manager) Handle(payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > MaxPayload {
		return nil, errors.New("job request exceeds payload limit")
	}
	var r Request
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil {
		return nil, errors.New("invalid job request")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing job request data")
	}
	response := m.Do(r)
	return json.Marshal(response)
}
func (m *Manager) Do(r Request) Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	fail := func(message string) Response { return Response{Error: message} }
	if m.closed || m.ctx.Err() != nil {
		return fail("job manager is closed")
	}
	if r.Action == "start" {
		return m.startLocked(r)
	}

	if r.OperationID != "" {
		if r.Action != "status" || !validID(r.OperationID) || r.ID != "" {
			return fail("operation_id requires status without a job ID and must be valid")
		}
		r.ID = m.operations[r.OperationID]
	}
	if r.ID != "" && !validID(r.ID) {
		return fail("invalid job ID")
	}

	if r.Action == "list" {
		if r.After != "" && !validID(r.After) {
			return fail("invalid list cursor")
		}
		limit := r.Limit
		if limit == 0 {
			limit = 128
		}
		if limit < 1 || limit > 128 {
			return fail("list limit must be 1..128")
		}
		values := make([]Job, 0, len(m.entries))
		for id, value := range m.entries {
			if id > r.After {
				values = append(values, copyJob(value.Job))
			}
		}
		sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
		response := Response{OK: true, Jobs: values}
		if len(values) > limit {
			response.Jobs = values[:limit]
			response.NextCursor = response.Jobs[limit-1].ID
		}
		return response
	}

	value, ok := m.entries[r.ID]
	if !ok {
		return fail("job not found")
	}
	snapshot := copyJob(value.Job)
	result := Response{OK: true, Job: &snapshot}
	switch r.Action {
	case "status":
		return result
	case "cancel":
		if value.State == "running" {
			value.cancel()
		}
		return result
	case "logs":
		if r.Limit == 0 {
			r.Limit = MaxRead
		}
		if r.Limit < 1 || r.Limit > MaxRead || r.StdoutCursor < 0 || r.StderrCursor < 0 {
			return fail("invalid log cursor or limit")
		}
		stdout, err := value.stdout.read(r.StdoutCursor, r.Limit)
		if err != nil {
			return fail(err.Error())
		}
		stderr, err := value.stderr.read(r.StderrCursor, r.Limit)
		if err != nil {
			return fail(err.Error())
		}
		result.Stdout = &stdout
		result.Stderr = &stderr
		return result
	default:
		return fail("unsupported job action")
	}
}
func (m *Manager) startLocked(r Request) Response {
	fail := func(message string) Response { return Response{Error: message} }
	if !validID(r.OperationID) || len(r.Command) == 0 || len(r.Command) > 64*1024 || strings.ContainsRune(r.Command, 0) || len(r.CWD) > 4096 || strings.ContainsRune(r.CWD, 0) {
		return fail("invalid job operation ID, command, or directory")
	}
	if len(r.Env) > 32 {
		return fail("too many job environment entries")
	}
	total := 0
	seen := map[string]bool{}
	for _, value := range r.Env {
		name, data, ok := strings.Cut(value, "=")
		if !ok || !validEnvName(name) || len(data) > 4096 || strings.ContainsRune(data, 0) {
			return fail("invalid job environment")
		}
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if seen[key] {
			return fail("duplicate job environment name")
		}
		seen[key] = true
		total += len(value)
	}
	if total > 16*1024 {
		return fail("job environment exceeds limit")
	}
	ttl := m.options.DefaultTTL
	if r.TimeoutMS != 0 {
		if r.TimeoutMS < 1 || r.TimeoutMS > m.options.MaxTTL.Milliseconds() {
			return fail("job timeout exceeds host limit")
		}
		ttl = time.Duration(r.TimeoutMS) * time.Millisecond
	}
	canonical := r
	canonical.Action = "start"
	canonical.ID = ""
	canonical.StdoutCursor = 0
	canonical.StderrCursor = 0
	canonical.Limit = 0
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	if id, ok := m.operations[r.OperationID]; ok {
		value := m.entries[id]
		if value.digest != digest {
			return fail("operation ID conflicts with different job content")
		}
		snapshot := copyJob(value.Job)
		return Response{OK: true, Job: &snapshot}
	}
	if len(m.entries) >= m.options.MaxJobs {
		return fail("job table capacity reached")
	}
	if m.running >= m.options.MaxConcurrent {
		return fail("job concurrency limit reached")
	}
	dir := r.CWD
	if dir == "" {
		dir = m.options.WorkDir
	} else if !filepath.IsAbs(dir) {
		if filepath.VolumeName(dir) != "" {
			return fail("drive-relative job directory is unsupported")
		}
		dir = filepath.Join(m.options.WorkDir, dir)
	}
	if dir != "" {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fail("job directory is unavailable")
		}
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fail("cannot generate job ID")
	}
	id := hex.EncodeToString(bytes)
	ctx, cancel := context.WithTimeout(m.ctx, ttl)
	value := &entry{Job: Job{ID: id, OperationID: r.OperationID, State: "running", CreatedAt: time.Now().UTC()}, digest: digest, cancel: cancel, stdout: ring{limit: m.options.LogBytes}, stderr: ring{limit: m.options.LogBytes}}
	m.entries[id] = value
	m.operations[r.OperationID] = id
	m.running++
	m.workers.Add(1)
	go m.run(ctx, value, r.Command, dir, r.Env)
	snapshot := copyJob(value.Job)
	return Response{OK: true, Job: &snapshot}
}
func (m *Manager) run(ctx context.Context, value *entry, command, dir string, env []string) {
	defer m.workers.Done()
	defer value.cancel()
	status := uint32(0)
	var failure error
	cmd, err := platform.Command(m.options.Shell, command, dir)
	if err == nil {
		cmd.Env = mergeEnv(os.Environ(), env)
		cmd.Stdin = nil
		cmd.Stdout = logWriter{m, value, false}
		cmd.Stderr = logWriter{m, value, true}
		cmd.WaitDelay = time.Second
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			stop, startErr := platform.Start(cmd)
			err = startErr
			if err == nil {
				done := make(chan struct{})
				go func() {
					select {
					case <-ctx.Done():
						stop()
					case <-done:
					}
				}()
				err = cmd.Wait()
				stop()
				close(done)
			}
		}
	}
	if err != nil {
		status = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if exit.ExitCode() >= 0 {
				status = uint32(exit.ExitCode())
			} else {
				status = 255
			}
		} else {
			failure = err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	value.ExitCode = &status
	value.FinishedAt = time.Now().UTC()
	switch {
	case m.ctx.Err() != nil:
		value.State = "cancelled"
	case ctx.Err() == context.DeadlineExceeded:
		value.State = "timed_out"
	case ctx.Err() != nil:
		value.State = "cancelled"
	case status == 0:
		value.State = "succeeded"
	default:
		value.State = "failed"
	}
	if failure != nil {
		value.Error = "job process could not start or complete"
	}
}
func copyJob(value Job) Job {
	if value.ExitCode != nil {
		code := *value.ExitCode
		value.ExitCode = &code
	}
	return value
}
func validID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func validEnvName(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for i, c := range value {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func mergeEnv(base, overrides []string) []string {
	result := append([]string(nil), base...)
	for _, value := range overrides {
		name, _, _ := strings.Cut(value, "=")
		found := false
		for i, old := range result {
			oldName, _, _ := strings.Cut(old, "=")
			equal := name == oldName
			if runtime.GOOS == "windows" {
				equal = strings.EqualFold(name, oldName)
			}
			if equal {
				result[i] = value
				found = true
				break
			}
		}
		if !found {
			result = append(result, value)
		}
	}
	return result
}

type ring struct {
	data       []byte
	start, end int64
	limit      int
}

func (r *ring) write(p []byte) {
	r.end += int64(len(p))
	if len(p) >= r.limit {
		r.data = append(r.data[:0], p[len(p)-r.limit:]...)
	} else {
		excess := len(r.data) + len(p) - r.limit
		if excess > 0 {
			copy(r.data, r.data[excess:])
			r.data = r.data[:len(r.data)-excess]
		}
		r.data = append(r.data, p...)
	}
	r.start = r.end - int64(len(r.data))
}
func (r *ring) read(cursor int64, limit int) (LogChunk, error) {
	if cursor > r.end {
		return LogChunk{}, errors.New("log cursor is ahead of available output")
	}
	dropped := int64(0)
	if cursor < r.start {
		dropped = r.start - cursor
		cursor = r.start
	}
	end := cursor + int64(limit)
	if end > r.end {
		end = r.end
	}
	return LogChunk{Data: append([]byte(nil), r.data[cursor-r.start:end-r.start]...), Cursor: end, Dropped: dropped}, nil
}

type logWriter struct {
	manager *Manager
	entry   *entry
	stderr  bool
}

func (w logWriter) Write(p []byte) (int, error) {
	w.manager.mu.Lock()
	defer w.manager.mu.Unlock()
	if w.stderr {
		w.entry.stderr.write(p)
	} else {
		w.entry.stdout.write(p)
	}
	return len(p), nil
}

// ValidateOperationID checks the opaque identifier without reflecting it in errors.
func ValidateOperationID(value string) error {
	if !validID(value) {
		return errors.New("operation-id must contain 1..128 ASCII letters, digits, underscore, or hyphen")
	}
	return nil
}
