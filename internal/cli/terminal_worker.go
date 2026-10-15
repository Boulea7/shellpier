package cli

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"golang.org/x/crypto/ssh"
)

type terminalPeer interface {
	Write([]byte) (int, error)
	Resize(int, int) error
	Signal(string) error
	Close() error
	Wait() error
}
type terminalSSHPeer struct {
	client  *ssh.Client
	session *ssh.Session
	input   io.WriteCloser
}

func (p *terminalSSHPeer) Write(data []byte) (int, error) { return p.input.Write(data) }
func (p *terminalSSHPeer) Resize(cols, rows int) error {
	ok, err := p.session.SendRequest("window-change", true, ssh.Marshal(struct{ Cols, Rows, Width, Height uint32 }{uint32(cols), uint32(rows), 0, 0}))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("remote rejected terminal resize")
	}
	return nil
}
func (p *terminalSSHPeer) Signal(signal string) error {
	ok, err := p.session.SendRequest("signal", true, ssh.Marshal(struct{ Signal string }{signal}))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("remote rejected terminal signal")
	}
	return nil
}
func (p *terminalSSHPeer) Close() error { return errors.Join(p.session.Close(), p.client.Close()) }
func (p *terminalSSHPeer) Wait() error  { return access.ConnectionError(p.client, p.session.Wait()) }
func terminalSSHConnect(ctx context.Context, state terminalState, config terminalConfig, ring io.Writer) (terminalPeer, error) {
	connection := access.DefaultConnectionOptions()
	if config.Connection != nil {
		connection = *config.Connection
	}
	if err := connection.Validate(); err != nil {
		return nil, err
	}
	ctx = access.WithConnectionOptions(ctx, connection)
	client, err := access.DialRetry(ctx, config.Ticket, config.Interface, config.Retry)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			client.Close()
		}
	}()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	session.Stdout, session.Stderr = ring, ring
	if err = configureSession(session, state.Workdir, config.Env); err != nil {
		return nil, err
	}
	if err = session.RequestPty(state.Term, state.Rows, state.Cols, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	if state.Command == "" {
		err = session.Shell()
	} else {
		err = session.Start(state.Command)
	}
	if err != nil {
		return nil, err
	}
	failed = false
	return &terminalSSHPeer{client: client, session: session, input: input}, nil
}
func terminalServeEntry(parent context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f := flags("terminal internal worker", stderr)
	path := f.String("state", "", "private state file")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 0 || *path == "" {
		return errors.New("invalid terminal worker invocation")
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	state, err := terminalReadState(absolute, true)
	if err != nil {
		return err
	}
	if state.ControlURL != "" {
		return errors.New("terminal worker state already claimed")
	}
	reader := bufio.NewReader(io.LimitReader(stdin, 128))
	nonce, err := reader.ReadString('\n')
	if err != nil || subtle.ConstantTimeCompare([]byte(nonce), []byte(state.Secret+"\n")) != 1 {
		return errors.New("terminal worker requires private parent handshake")
	}
	cfgPath := filepath.Join(filepath.Dir(absolute), "config.json")
	if err = checkPrivateSessionFile(cfgPath); err != nil {
		return err
	}
	data, err := readRegular(cfgPath, terminalFileLimit)
	if err != nil {
		return err
	}
	var config terminalConfig
	if json.Unmarshal(data, &config) != nil {
		return errors.New("invalid terminal worker configuration")
	}
	if config.Connection != nil {
		if err = config.Connection.Validate(); err != nil {
			return err
		}
	}
	if err = config.Ticket.Validate("access"); err != nil {
		return err
	}
	if err = config.Retry.Validate(); err != nil {
		return err
	}
	var validatedEnv environmentValues
	for _, entry := range config.Env {
		if err = validatedEnv.Set(entry); err != nil {
			return err
		}
	}
	// Persistent tickets still receive a finite default worker deadline.
	expiry := state.ExpiresAt
	if expiry.After(time.Now().Add(24 * time.Hour)) {
		return errors.New("terminal worker expiry exceeds bound")
	}
	if !config.Ticket.ExpiresAt.IsZero() && expiry.After(config.Ticket.ExpiresAt) {
		return errors.New("terminal worker would extend authorization")
	}
	ctx, cancel := context.WithDeadline(parent, expiry)
	defer cancel()
	committed := make(chan struct{})
	go func() {
		ack, e := reader.ReadString('\n')
		if e != nil || ack != "commit\n" {
			cancel()
			return
		}
		close(committed)
	}()
	setupTimer := time.AfterFunc(30*time.Second, cancel)
	defer setupTimer.Stop()
	ring := newTerminalRing()
	if config.LogLimit != 0 {
		log, err := newTerminalLog(filepath.Dir(absolute), config.LogLimit)
		if err != nil {
			return err
		}
		ring.log = log
		defer log.stop()
	}
	peer, err := terminalSSHConnect(ctx, state, config, ring)
	if err != nil {
		return errors.New("terminal SSH setup failed; no remote operation was replayed")
	}
	defer peer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	state.ControlURL = "http://" + listener.Addr().String()
	if err = terminalReplacePrivate(absolute, state); err != nil {
		return err
	}
	// The access ticket is needed only for the initial connection.
	if err = os.Remove(cfgPath); err != nil {
		return err
	}
	runtime := newTerminalRuntime(ctx, state, absolute, peer, ring)
	server := &http.Server{Handler: runtime, ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 40 * time.Second, MaxHeaderBytes: 4096}
	defer server.Close()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	if err = json.NewEncoder(stdout).Encode(map[string]bool{"ready": true}); err != nil {
		return err
	}
	select {
	case <-committed:
		setupTimer.Stop()
	case <-ctx.Done():
		runtime.end(-1)
	}

	select {
	case <-ctx.Done():
		runtime.end(-1)
	case <-runtime.done:
	case <-serverDone:
		runtime.end(-1)
	}
	// Finish an acknowledged close response before dropping the control socket.
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShutdown()
	server.Shutdown(shutdown)
	return runtime.snapshotError()
}

type terminalRuntime struct {
	ctx        context.Context
	state      terminalState
	statePath  string
	peer       terminalPeer
	ring       *terminalRing
	op         chan struct{}
	done       chan struct{}
	waitDone   chan struct{}
	once       sync.Once
	mu         sync.Mutex
	persistErr error
	operations map[string]*terminalOperationEntry
}

func newTerminalRuntime(ctx context.Context, state terminalState, path string, peer terminalPeer, ring *terminalRing) *terminalRuntime {
	r := &terminalRuntime{ctx: ctx, state: state, statePath: path, peer: peer, ring: ring, op: make(chan struct{}, 1), done: make(chan struct{}), waitDone: make(chan struct{})}
	go func() {
		err := peer.Wait()
		code := 0
		if err != nil {
			code = -1
			var exit *ssh.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitStatus()
			}
		}
		close(r.waitDone)
		reason, known := executionOutcome(err)
		if !known && ctx.Err() != nil {
			reason, known = executionOutcome(ctx.Err())
		}
		r.endOutcome(code, reason, known)
	}()
	return r
}
func (r *terminalRuntime) end(code int) {
	reason := "canceled"
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		reason = "expired"
	}
	r.endOutcome(code, reason, false)
}
func (r *terminalRuntime) endOutcome(code int, reason string, known bool) {
	r.once.Do(func() {
		r.peer.Close()
		select {
		case <-r.waitDone:
		case <-time.After(2 * time.Second):
		}
		r.ring.finishOutcome(code, reason, known)
		if r.ring.log != nil {
			r.ring.log.stop()
		}
		r.mu.Lock()
		snapshot := r.ring.snapshot()
		snapshot.SessionID = r.state.ID
		r.persistErr = writeJSONPrivate(filepath.Join(filepath.Dir(r.statePath), "snapshot.json"), snapshot)
		r.mu.Unlock()
		close(r.done)
	})
}
func (r *terminalRuntime) snapshotError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.persistErr != nil {
		return errors.New("terminal closed but private output snapshot could not be saved")
	}
	return nil
}
func (r *terminalRuntime) metadata() terminalMetadata {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	return terminalMeta(state, r.statePath, r.ring.snapshot())
}
func (r *terminalRuntime) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if !terminalAuthorized(request, r.state.Secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		OperationID string `json:"operation_id"`
		Cursor      uint64 `json:"cursor"`
		Limit       int    `json:"limit"`
		WaitMs      int64  `json:"wait_ms"`
		Data        []byte `json:"data"`
		Cols        int    `json:"cols"`
		Rows        int    `json:"rows"`
		Signal      string `json:"signal"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 2*terminalCapacity))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid request", 400)
		return
	}
	if request.URL.RawQuery != "" {
		http.Error(w, "invalid request", 400)
		return
	}
	switch request.URL.Path {
	case "/read":
		if body.WaitMs < 0 || body.WaitMs > 30000 {
			http.Error(w, "invalid wait", 400)
			return
		}
		result, err := r.ring.read(request.Context(), body.Cursor, body.Limit, time.Duration(body.WaitMs)*time.Millisecond)
		if err != nil {
			http.Error(w, "invalid read", 400)
			return
		}
		json.NewEncoder(w).Encode(result)
		return
	case "/operation":
		result, ok := r.operationResult(body.OperationID)
		if !ok {
			http.Error(w, "operation unknown in this worker", 404)
			return
		}
		json.NewEncoder(w).Encode(result)
		return
	case "/status":
		json.NewEncoder(w).Encode(r.metadata())
		return
	case "/close":
		r.end(-1)
		if r.snapshotError() != nil {
			http.Error(w, "snapshot failed", 500)
			return
		}
		json.NewEncoder(w).Encode(r.metadata())
		return
	case "/write", "/resize", "/signal":
	default:
		http.NotFound(w, request)
		return
	}
	if len(body.Data) > terminalCapacity || (request.URL.Path == "/resize" && terminalDimensions(body.Cols, body.Rows) != nil) || (request.URL.Path == "/signal" && !terminalSignalAllowed(body.Signal)) {
		http.Error(w, "invalid operation", 400)
		return
	}
	var entry *terminalOperationEntry
	if request.URL.Path == "/write" && body.OperationID != "" {
		var cached bool
		var err error
		entry, cached, err = r.beginOperation(body.OperationID, body.Data)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		if cached {
			result, _ := r.operationResult(body.OperationID)
			json.NewEncoder(w).Encode(result)
			return
		}
	}
	// Before dispatch, a canceled request has not sent input; keep its ID uncertain.
	if entry != nil {
		defer r.finishOperation(entry, "uncertain", 0)
	}
	select {
	case r.op <- struct{}{}:
	case <-request.Context().Done():
		return
	case <-r.done:
		http.Error(w, "closed", 409)
		return
	}
	defer func() { <-r.op }()
	select {
	case <-r.done:
		http.Error(w, "closed", 409)
		return
	default:
	}
	operation, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		switch request.URL.Path {
		case "/write":
			n, err := r.peer.Write(body.Data)
			if err == nil && n != len(body.Data) {
				err = io.ErrShortWrite
			}
			if entry != nil {
				status := "completed"
				if err != nil {
					status = "uncertain"
				}
				r.finishOperation(entry, status, n)
			}
			completed <- err
		case "/resize":
			completed <- r.peer.Resize(body.Cols, body.Rows)
		case "/signal":
			completed <- r.peer.Signal(body.Signal)
		}
	}()
	var err error
	select {
	case err = <-completed:
	case <-operation.Done():
		err = operation.Err()
		r.end(-1)
	case <-r.ctx.Done():
		err = r.ctx.Err()
		r.end(-1)
	}
	if err != nil {
		http.Error(w, "terminal operation failed; it was not replayed", 502)
		return
	}
	if request.URL.Path == "/resize" {
		r.mu.Lock()
		r.state.Cols = body.Cols
		r.state.Rows = body.Rows
		state := r.state
		r.mu.Unlock()
		if err = terminalReplacePrivate(r.statePath, state); err != nil {
			r.end(-1)
			http.Error(w, "state update failed", 500)
			return
		}
	}
	if entry != nil {
		result, _ := r.operationResult(body.OperationID)
		json.NewEncoder(w).Encode(result)
		return
	}
	json.NewEncoder(w).Encode(r.metadata())
}
