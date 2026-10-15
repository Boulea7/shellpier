package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"
)

const terminalOperationCapacity = 256

type terminalOperationResult struct {
	ID             string    `json:"operation_id"`
	Status         string    `json:"status"`
	BytesWritten   int       `json:"bytes_written"`
	DedupExpiresAt time.Time `json:"dedup_expires_at,omitempty"`
	DedupScope     string    `json:"dedup_scope"`
}
type terminalOperationEntry struct {
	result  terminalOperationResult
	digest  [32]byte
	created time.Time
}

func terminalOperationID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (r *terminalRuntime) beginOperation(id string, data []byte) (*terminalOperationEntry, bool, error) {
	if !terminalOperationID(id) {
		return nil, false, errors.New("invalid operation ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	digest := sha256.Sum256(data)
	if e := r.operations[id]; e != nil {
		if e.digest != digest {
			return nil, false, errors.New("operation ID content conflict")
		}
		return e, true, nil
	}
	if len(r.operations) >= terminalOperationCapacity {
		return nil, false, errors.New("operation cache full; existing IDs remain queryable for this worker lifetime")
	}
	if r.operations == nil {
		r.operations = make(map[string]*terminalOperationEntry)
	}
	e := &terminalOperationEntry{terminalOperationResult{ID: id, Status: "running", DedupExpiresAt: r.state.ExpiresAt, DedupScope: "worker_lifetime"}, digest, time.Now()}
	r.operations[id] = e
	return e, false, nil
}
func (r *terminalRuntime) finishOperation(e *terminalOperationEntry, status string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.result.Status == "running" {
		e.result.Status = status
		e.result.BytesWritten = n
	}
}
func (r *terminalRuntime) operationResult(id string) (terminalOperationResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.operations[id]
	if e == nil {
		return terminalOperationResult{}, false
	}
	return e.result, true
}

// Wait observes whole-terminal completion, never individual shell commands.
func terminalWait(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("terminal wait", flag.ContinueOnError)
	f.SetOutput(stderr)
	path := f.String("state", "", "private state file")
	wait := f.Duration("wait", 30*time.Second, "bounded wait for terminal closure, at most 30s")
	machine := f.Bool("json", false, "JSON metadata")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 0 || *path == "" || *wait < 0 || *wait > 30*time.Second {
		return errors.New("wait requires --state and --wait 0..30s")
	}
	waitCtx := ctx
	if *wait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, *wait)
		defer cancel()
	}
	deadline := time.NewTimer(*wait)
	defer deadline.Stop()
	var last *terminalMetadata
	for {
		var out terminalMetadata
		buffer := &bytes.Buffer{}
		if err := terminalControl(waitCtx, "status", []string{"--state", *path, "--json"}, stdin, buffer, stderr); err != nil {
			if ctx.Err() == nil && waitCtx.Err() != nil && last != nil {
				return terminalPrintMetadata(stdout, *machine, *last)
			}
			return err
		}
		if err := json.Unmarshal(buffer.Bytes(), &out); err != nil {
			return err
		}
		last = &out
		if out.Closed {
			return terminalPrintMetadata(stdout, *machine, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return terminalPrintMetadata(stdout, *machine, out)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
