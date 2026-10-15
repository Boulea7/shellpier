package cli

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
)

const terminalFileLimit = 512 * 1024

type terminalState struct {
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Secret     string    `json:"secret"`
	ControlURL string    `json:"control_url"`
	ExpiresAt  time.Time `json:"expires_at"`
	Term       string    `json:"term"`
	Cols       int       `json:"cols"`
	Rows       int       `json:"rows"`
	Command    string    `json:"command,omitempty"`
	Workdir    string    `json:"workdir,omitempty"`
}
type terminalConfig struct {
	Connection *access.ConnectionOptions `json:"connection,omitempty"`
	LogLimit   int64                     `json:"log_limit,omitempty"`
	Ticket     access.Ticket             `json:"ticket"`
	Interface  string                    `json:"interface,omitempty"`
	Retry      access.RetryOptions       `json:"retry"`
	Env        []string                  `json:"env,omitempty"`
}
type terminalMetadata struct {
	Log               *terminalLogStatus `json:"log,omitempty"`
	TerminationReason string             `json:"termination_reason,omitempty"`
	ExitStatusKnown   bool               `json:"exit_status_known"`
	StatePath         string             `json:"state_path"`
	ID                string             `json:"id"`
	ExpiresAt         time.Time          `json:"expires_at"`
	Term              string             `json:"term"`
	Cols              int                `json:"cols"`
	Rows              int                `json:"rows"`
	Closed            bool               `json:"closed"`
	ExitCode          *int               `json:"exit_code,omitempty"`
	NextCursor        uint64             `json:"next_cursor"`
}

func terminalMeta(state terminalState, path string, snapshot terminalReadResult) terminalMetadata {
	return terminalMetadata{Log: snapshot.Log, TerminationReason: snapshot.TerminationReason, ExitStatusKnown: snapshot.ExitStatusKnown, StatePath: path, ID: state.ID, ExpiresAt: state.ExpiresAt, Term: state.Term, Cols: state.Cols, Rows: state.Rows, Closed: snapshot.Closed, ExitCode: snapshot.ExitCode, NextCursor: snapshot.NextCursor}
}

func terminalDimensions(cols, rows int) error {
	if cols < 1 || cols > 1000 || rows < 1 || rows > 1000 {
		return errors.New("terminal dimensions must be 1..1000")
	}
	return nil
}
func terminal(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, "terminal open TICKET --out NEW-PRIVATE-DIR [--command SHELL] [--timeout 3h]\nterminal read|write|resize|signal|status|close|wait|operation --state FILE\nread returns base64 bytes with absolute cursors; closed output remains in a bounded private snapshot.\n")
		return err
	}
	switch args[0] {
	case "open":
		return terminalOpen(ctx, args[1:], stdout, stderr)
	case "wait":
		return terminalWait(ctx, args[1:], stdin, stdout, stderr)
	case "serve":
		return terminalServeEntry(ctx, args[1:], stdin, stdout, stderr)
	case "read", "write", "resize", "signal", "status", "close", "operation":
		return terminalControl(ctx, args[0], args[1:], stdin, stdout, stderr)
	default:
		return errors.New("unknown terminal operation")
	}
}
func terminalReadState(path string, allowPending bool) (terminalState, error) {
	var state terminalState
	if filepath.Base(path) != "state.json" {
		return state, errors.New("terminal state must be an owned state.json")
	}
	if err := checkPrivateSessionDir(filepath.Dir(path)); err != nil {
		return state, err
	}
	if err := checkPrivateSessionFile(path); err != nil {
		return state, err
	}
	data, err := terminalReadStateBytes(path, readRegular)
	if err != nil {
		return state, err
	}
	if json.Unmarshal(data, &state) != nil || state.Version != 1 || state.Kind != "shellpier-terminal" || len(state.ID) != 64 || len(state.Secret) != 64 || state.ExpiresAt.IsZero() {
		return state, errors.New("invalid terminal state")
	}
	if _, err = hex.DecodeString(state.ID); err != nil {
		return state, errors.New("invalid terminal identity")
	}
	if _, err = hex.DecodeString(state.Secret); err != nil {
		return state, errors.New("invalid terminal token")
	}
	if err = terminalDimensions(state.Cols, state.Rows); err != nil {
		return state, err
	}
	if len(state.Term) < 1 || len(state.Term) > 64 || strings.IndexFunc(state.Term, func(c rune) bool { return c < 33 || c > 126 }) >= 0 || len(state.Command) > 64*1024 || len(state.Workdir) > 4096 || strings.ContainsRune(state.Command, 0) || strings.ContainsRune(state.Workdir, 0) || !utf8.ValidString(state.Command) || !utf8.ValidString(state.Workdir) {
		return state, errors.New("invalid terminal state options")
	}
	if state.ControlURL == "" && allowPending {
		return state, nil
	}
	u, err := url.Parse(state.ControlURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.RawPath != "" {
		return state, errors.New("invalid terminal control endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || u.Host != net.JoinHostPort("127.0.0.1", u.Port()) {
		return state, errors.New("invalid terminal control endpoint")
	}
	return state, nil
}

// A legitimate atomic update can move the pathname between ReadFile's lstat/open.
// Retry only an observed identity change, rechecking owner and private permissions.
func terminalReadStateBytes(path string, read func(string, int64) ([]byte, error)) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		if err := checkPrivateSessionDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
		if err := checkPrivateSessionFile(path); err != nil {
			return nil, err
		}
		before, err := terminalFileIdentity(path)
		if err != nil {
			return nil, err
		}
		data, err := read(path, terminalFileLimit)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, access.ErrFileChanged) {
			return nil, err
		}
		if e := checkPrivateSessionDir(filepath.Dir(path)); e != nil {
			return nil, e
		}
		if e := checkPrivateSessionFile(path); e != nil {
			return nil, e
		}
		after, e := terminalFileIdentity(path)
		if e != nil || os.SameFile(before, after) {
			return nil, err
		}
		last = err
		if attempt < 7 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("terminal state did not stabilize after bounded reread: %w", last)
}

func terminalReplacePrivate(path string, value any) error {
	if err := checkPrivateSessionDir(filepath.Dir(path)); err != nil {
		return err
	}
	old, err := terminalFileIdentity(path)
	if err != nil {
		return err
	}
	if err = checkPrivateSessionFile(path); err != nil {
		return err
	}
	nonce, err := secret()
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".terminal-"+nonce+".tmp")
	if err = writeJSONPrivate(tmp, value); err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Local persistence may contend with a Windows reader that denies delete sharing.
	// Retry only the rename, never the already acknowledged remote operation.
	deadline := time.Now().Add(time.Second)
	for {
		current, e := terminalFileIdentity(path)
		if e != nil || !os.SameFile(old, current) {
			return errors.New("terminal state changed unexpectedly")
		}
		if e = checkPrivateSessionFile(path); e != nil {
			return e
		}
		err = os.Rename(tmp, path)
		if err == nil || !terminalRetryReplace(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}

}
func terminalOpen(ctx context.Context, args []string, stdout, stderr io.Writer) (result error) {
	f := flags("terminal open", stderr)
	out := f.String("out", "", "new private terminal directory")
	command := f.String("command", "", "exact remote shell command; empty starts default shell")
	workdir := f.String("cwd", "", "remote working directory")
	cols := f.Int("cols", 80, "terminal columns")
	rows := f.Int("rows", 24, "terminal rows")
	term := f.String("term", "xterm-256color", "TERM name")
	timeout := f.Duration("timeout", 3*time.Hour, "terminal lifetime, bounded by ticket expiry")
	logLimit := f.Int64("log-limit", 0, "optional private output.log byte limit, at most 64 MiB")
	iface := f.String("interface", "", "direct routing interface")
	machine := f.Bool("json", false, "emit metadata as JSON")
	retry := clientRetryFlags(f)
	var env environmentValues
	f.Var(&env, "env", "explicit remote NAME=VALUE; repeatable")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 1 || *out == "" {
		return errors.New("terminal open requires one ticket and --out NEW-PRIVATE-DIR")
	}
	if err := terminalDimensions(*cols, *rows); err != nil {
		return err
	}
	if *logLimit < 0 || *logLimit > terminalLogMax {
		return errors.New("log limit must be 0..67108864")
	}
	if *timeout <= 0 || *timeout > 24*time.Hour {
		return errors.New("terminal timeout must be positive and at most 24h")
	}
	if len(*term) == 0 || len(*term) > 64 || strings.IndexFunc(*term, func(c rune) bool { return c < 33 || c > 126 }) >= 0 || len(*command) > 64*1024 || len(*workdir) > 4096 || strings.ContainsRune(*command, 0) || strings.ContainsRune(*workdir, 0) || !utf8.ValidString(*command) || !utf8.ValidString(*workdir) {
		return errors.New("invalid terminal options")
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	ctx = retry.Context(ctx)
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err = ticket.Validate("access"); err != nil {
		return err
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if info, e := os.Stat(parent); e != nil || !info.IsDir() {
		return errors.New("terminal output parent must exist")
	}
	if err = os.Mkdir(path, 0700); err != nil {
		return fmt.Errorf("create new terminal directory: %w", err)
	}
	created, err := terminalFileIdentity(path)
	if err != nil {
		return err
	}
	knownFiles := make(map[string]os.FileInfo)
	var expectedState terminalState
	committed := false
	defer func() {
		if !committed {
			terminalRemoveStartup(path, created, knownFiles, expectedState)
		}
	}()
	if err = platform.SecureDir(path); err != nil {
		return err
	}
	id, err := secret()
	if err != nil {
		return err
	}
	token, err := secret()
	if err != nil {
		return err
	}
	expiry := time.Now().Add(*timeout)
	if !ticket.ExpiresAt.IsZero() && ticket.ExpiresAt.Before(expiry) {
		expiry = ticket.ExpiresAt
	}
	state := terminalState{Version: 1, Kind: "shellpier-terminal", ID: id, Secret: token, ExpiresAt: expiry, Term: *term, Cols: *cols, Rows: *rows, Command: *command, Workdir: *workdir}
	expectedState = state
	statePath := filepath.Join(path, "state.json")
	if err = writeJSONPrivate(statePath, state); err != nil {
		return err
	}
	knownFiles["state.json"], _ = terminalFileIdentity(statePath)
	if err = writeJSONPrivate(filepath.Join(path, "config.json"), terminalConfig{Ticket: ticket, Interface: *iface, Retry: retry.RetryOptions, Connection: &retry.Connection, Env: env, LogLimit: *logLimit}); err != nil {
		return err
	}
	knownFiles["config.json"], _ = terminalFileIdentity(filepath.Join(path, "config.json"))
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "terminal", "serve", "--state", statePath)
	configureTerminalWorker(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	defer output.Close()
	// Startup diagnostics are bounded and never printed verbatim with credentials.
	diagnostic := newTerminalRing()
	cmd.Stderr = diagnostic
	if err = cmd.Start(); err != nil {
		input.Close()
		return err
	}
	waited := make(chan error, 1)
	var waitOnce sync.Once
	startWait := func() { waitOnce.Do(func() { go func() { waited <- cmd.Wait() }() }) }
	ready := make(chan error, 1)
	go func() {
		if _, e := io.WriteString(input, token+"\n"); e != nil {
			ready <- e
			return
		}
		var reply struct {
			Ready bool `json:"ready"`
		}
		if e := json.NewDecoder(io.LimitReader(output, 8192)).Decode(&reply); e != nil || !reply.Ready {
			ready <- errors.New("terminal worker failed to become ready")
			return
		}
		ready <- nil
	}()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case err = <-ready:
	case <-startup.Done():
		err = startup.Err()
	}
	if err != nil {
		output.Close()
		input.Close()
		cmd.Process.Kill()
		startWait()
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
		}
		return err
	}
	output.Close()
	startWait()
	fresh, err := terminalReadState(statePath, false)
	if err != nil {
		cmd.Process.Kill()
		return err
	}
	snapshot, _ := terminalSnapshotFile(filepath.Join(path, "snapshot.json"))
	meta := terminalMeta(fresh, statePath, snapshot)
	if *machine {
		err = json.NewEncoder(stdout).Encode(meta)
	} else {
		_, err = fmt.Fprintf(stdout, "state_path: %s\nexpires_at: %s\n", statePath, expiry.Format(time.RFC3339))
	}
	if err == nil {
		if _, e := io.WriteString(input, "commit\n"); e != nil {
			err = errors.New("terminal worker commit failed")
		}
	}
	input.Close()
	if err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = terminalControl(closeCtx, "close", []string{"--state", statePath}, strings.NewReader(""), io.Discard, io.Discard)
		closeCancel()
		cmd.Process.Kill()
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
		}
	} else {
		committed = true
	}
	return err
}
func terminalRemoveStartup(dir string, created os.FileInfo, known map[string]os.FileInfo, expected terminalState) {
	current, err := terminalFileIdentity(dir)
	if err != nil || !os.SameFile(created, current) {
		return
	}
	for _, name := range []string{"state.json", "config.json", "snapshot.json"} {
		path := filepath.Join(dir, name)
		if checkPrivateSessionFile(path) != nil {
			continue
		}
		before, err := terminalFileIdentity(path)
		if err != nil {
			continue
		}
		owned := known[name] != nil && os.SameFile(known[name], before)
		if name == "state.json" && !owned {
			state, e := terminalReadState(path, true)
			owned = e == nil && expected.ID != "" && state.ID == expected.ID && state.Secret == expected.Secret
		}
		if name == "snapshot.json" {
			snapshot, e := terminalSnapshotFile(path)
			owned = e == nil && expected.ID != "" && snapshot.SessionID == expected.ID
		}
		after, err := terminalFileIdentity(path)
		if owned && err == nil && os.SameFile(before, after) {
			os.Remove(path)
		}
	}
	os.Remove(dir) // Unrelated files prevent directory removal.
}
func terminalControl(ctx context.Context, mode string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f := flags("terminal "+mode, stderr)
	statePath := f.String("state", "", "private terminal state file")
	machine := f.Bool("json", false, "emit metadata as JSON")
	var cursorValue uint64
	limitValue := terminalDefaultRead
	var waitValue time.Duration
	var textValue, fileValue, signalValue string
	colsValue, rowsValue := 80, 24
	cursor, limit, wait, text, file, cols, rows, signal := &cursorValue, &limitValue, &waitValue, &textValue, &fileValue, &colsValue, &rowsValue, &signalValue
	operationID := ""
	if mode == "write" || mode == "operation" {
		f.StringVar(&operationID, "operation-id", "", "unique input ID; retained for this worker lifetime, at most 256 IDs")
	}
	switch mode {
	case "read":
		f.Uint64Var(cursor, "cursor", 0, "absolute output byte offset")
		f.IntVar(limit, "limit", terminalDefaultRead, "maximum read bytes")
		f.DurationVar(wait, "wait", 0, "wait for output, at most 30s")
	case "write":
		f.StringVar(text, "text", "", "exact bytes to send")
		f.StringVar(file, "file", "", "input file, or - for stdin")
	case "resize":
		f.IntVar(cols, "cols", 80, "terminal columns")
		f.IntVar(rows, "rows", 24, "terminal rows")
	case "signal":
		f.StringVar(signal, "signal", "", "SSH signal name")
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 0 || *statePath == "" {
		return errors.New("terminal operation requires --state FILE")
	}
	if *limit < 1 || *limit > terminalCapacity || *wait < 0 || *wait > 30*time.Second {
		return errors.New("read limit must be 1..1048576 and wait 0..30s")
	}
	path, err := filepath.Abs(*statePath)
	if err != nil {
		return err
	}
	state, err := terminalReadState(path, false)
	if err != nil {
		return err
	}
	if operationID != "" && !terminalOperationID(operationID) {
		return errors.New("invalid operation ID")
	}
	if mode == "operation" && operationID == "" {
		return errors.New("operation requires --operation-id")
	}
	body := map[string]any{"operation_id": operationID}
	switch mode {
	case "read":
		body = map[string]any{"cursor": *cursor, "limit": *limit, "wait_ms": wait.Milliseconds()}
	case "write":
		hasText := false
		f.Visit(func(v *flag.Flag) {
			if v.Name == "text" {
				hasText = true
			}
		})
		if hasText == (*file != "") {
			return errors.New("write requires exactly one of --text or --file")
		}
		data := []byte(*text)
		if *file != "" {
			if *file == "-" {
				data, err = io.ReadAll(io.LimitReader(stdin, terminalCapacity+1))
			} else {
				data, err = readRegular(*file, terminalCapacity)
			}
		}
		if err != nil {
			return err
		}
		if len(data) > terminalCapacity {
			return errors.New("write exceeds 1 MiB")
		}
		body = map[string]any{"data": data, "operation_id": operationID}
	case "resize":
		if err = terminalDimensions(*cols, *rows); err != nil {
			return err
		}
		body = map[string]any{"cols": *cols, "rows": *rows}
	case "signal":
		if !terminalSignalAllowed(*signal) {
			return errors.New("unsupported SSH terminal signal")
		}
		body = map[string]any{"signal": *signal}
	}
	// A closed bounded snapshot remains usable without a worker process.
	snapshotPath := filepath.Join(filepath.Dir(path), "snapshot.json")
	readSnapshot := func() (bool, error) {
		if mode != "read" && mode != "status" && mode != "close" {
			return false, nil
		}
		if snapshot, e := terminalSnapshotFile(snapshotPath); e == nil {
			if snapshot.SessionID != state.ID {
				return true, errors.New("terminal snapshot belongs to another session")
			}
			if mode == "read" {
				r, e := terminalReadSnapshot(snapshot, *cursor, *limit)
				if e != nil {
					return true, e
				}
				return true, json.NewEncoder(stdout).Encode(r)
			}
			return true, terminalPrintMetadata(stdout, *machine, terminalMeta(state, path, snapshot))
		}
		return false, nil
	}
	if found, err := readSnapshot(); found {
		return err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, *wait+10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(operationCtx, http.MethodPost, state.ControlURL+"/"+mode, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.Secret)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		// The worker persists its final snapshot before closing the listener.
		// It may have exited between our initial snapshot check and request.
		if found, snapshotErr := readSnapshot(); found {
			return snapshotErr
		}
		return errors.New("terminal worker unavailable; no operation was replayed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*terminalCapacity+1))
	if err != nil {
		if found, snapshotErr := readSnapshot(); found {
			return snapshotErr
		}
		return err
	}
	if len(data) > 2*terminalCapacity {
		return errors.New("terminal response exceeded bound")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("terminal operation refused (HTTP %d)", resp.StatusCode)
	}
	if mode == "operation" || (mode == "write" && operationID != "") {
		var result terminalOperationResult
		if json.Unmarshal(data, &result) != nil || result.ID != operationID {
			return errors.New("invalid operation response")
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	if mode == "read" {
		var r terminalReadResult
		if json.Unmarshal(data, &r) != nil || len(r.Data) > *limit {
			return errors.New("invalid terminal read response")
		}
		return json.NewEncoder(stdout).Encode(r)
	}
	var meta terminalMetadata
	if json.Unmarshal(data, &meta) != nil || meta.ID != state.ID {
		return errors.New("invalid terminal metadata response")
	}
	return terminalPrintMetadata(stdout, *machine, meta)
}
func terminalPrintMetadata(stdout io.Writer, machine bool, meta terminalMetadata) error {
	if machine {
		return json.NewEncoder(stdout).Encode(meta)
	}
	_, err := fmt.Fprintf(stdout, "state_path: %s\nclosed: %t\nnext_cursor: %d\n", meta.StatePath, meta.Closed, meta.NextCursor)
	return err
}
func terminalSnapshotFile(path string) (terminalReadResult, error) {
	var result terminalReadResult
	if err := checkPrivateSessionFile(path); err != nil {
		return result, err
	}
	data, err := readRegular(path, 2*terminalCapacity)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil || !result.Closed || len(result.Data) > terminalCapacity {
		return result, errors.New("invalid terminal snapshot")
	}
	return result, nil
}
func terminalSignalAllowed(signal string) bool {
	switch signal {
	case "INT", "TERM", "KILL", "HUP", "QUIT", "USR1", "USR2":
		return true
	}
	return false
}

// Constant-time token comparison is shared with worker control handlers.
func terminalAuthorized(request *http.Request, token string) bool {
	return subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+token)) == 1
}
