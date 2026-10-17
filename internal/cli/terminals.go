package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/terminals"
	"golang.org/x/crypto/ssh"
)

// TerminalOperationUnknownError preserves the keys needed to inspect a submitted mutation.
type TerminalOperationUnknownError struct {
	Action      string
	OperationID string
	TerminalID  string
	Err         error
}

func (e *TerminalOperationUnknownError) Error() string {
	query := fmt.Sprintf("terminals operation TICKET %s", e.OperationID)
	if e.Action == "write" {
		query += fmt.Sprintf(" --id %s", e.TerminalID)
	}
	return fmt.Sprintf("terminal %s result unknown; query with %s: %v", e.Action, query, e.Err)
}
func (e *TerminalOperationUnknownError) Unwrap() error { return e.Err }

type terminalsRejectedError struct{ message string }

func (e *terminalsRejectedError) Error() string { return e.message }

func terminalsCommand(parent context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier terminals open|read|write|status|list|resize|signal|close|operation TICKET [ID] [flags]\nHost-owned terminals survive controller disconnects and end with the host. Query uncertain operations before retrying.")
		return err
	}
	action := args[0]
	switch action {
	case "open", "read", "write", "status", "list", "resize", "signal", "close", "operation":
	default:
		return errors.New("unknown terminals action")
	}
	f := flags("terminals "+action, stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 20*time.Second, "total control call timeout, at most 5m")
	asJSON := f.Bool("json", false, "emit bounded terminal response")
	r := terminals.Request{Version: 1, Action: action}
	var lifetime time.Duration
	var env environmentValues
	var text, file string
	if action == "open" {
		f.StringVar(&r.Command, "command", "", "exact remote shell command; empty starts shell")
		f.StringVar(&r.CWD, "cwd", "", "remote working directory")
		f.Var(&env, "env", "remote NAME=VALUE; repeatable")
		f.StringVar(&r.Term, "term", "xterm-256color", "terminal type")
		f.DurationVar(&lifetime, "lifetime", 3*time.Hour, "terminal lifetime, at most 24h")
	}
	if action == "open" || action == "resize" {
		f.IntVar(&r.Cols, "cols", 80, "terminal columns")
		f.IntVar(&r.Rows, "rows", 24, "terminal rows")
	}
	if action == "open" || action == "write" {
		f.StringVar(&r.OperationID, "operation-id", "", "stable mutation deduplication ID")
	}
	if action == "write" {
		f.StringVar(&text, "text", "", "terminal input text")
		f.StringVar(&file, "file", "", "local input file, or - for stdin")
	}
	if action == "read" {
		f.Uint64Var(&r.Cursor, "cursor", 0, "absolute byte cursor")
		f.IntVar(&r.Limit, "limit", terminals.MaxRead, "maximum output bytes")
	}
	if action == "signal" {
		f.StringVar(&r.Signal, "signal", "", "terminal signal")
	}
	if action == "operation" {
		f.StringVar(&r.ID, "id", "", "terminal ID for write operation lookup")
	}
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("timeout must be positive and at most 5m")
	}
	expected := 2
	if action == "open" || action == "list" {
		expected = 1
	}
	if f.NArg() != expected {
		return errors.New("provide an access ticket and, except open/list, exactly one ID")
	}
	if action == "operation" {
		r.OperationID = f.Arg(1)
		if err := terminals.ValidateOperationID(r.OperationID); err != nil {
			return err
		}
	} else if expected == 2 {
		r.ID = f.Arg(1)
	}
	if r.ID != "" {
		if err := terminals.ValidateOperationID(r.ID); err != nil {
			return errors.New("invalid terminal ID")
		}
	}
	if action == "open" || action == "resize" {
		if r.Cols < 1 || r.Cols > 1000 || r.Rows < 1 || r.Rows > 1000 {
			return errors.New("cols and rows must be between 1 and 1000")
		}
	}
	if action == "open" {
		if lifetime < time.Millisecond || lifetime > 24*time.Hour {
			return errors.New("lifetime must be between 1ms and 24h")
		}
		r.TimeoutMS = lifetime.Milliseconds()
		r.Env = env
	}
	if action == "read" && (r.Limit < 1 || r.Limit > terminals.MaxRead) {
		return errors.New("limit must be between 1 and 32768")
	}
	if action == "signal" {
		switch r.Signal {
		case "ABRT", "ALRM", "FPE", "HUP", "ILL", "INT", "KILL", "PIPE", "QUIT", "SEGV", "TERM", "USR1", "USR2":
		default:
			return errors.New("invalid terminal signal")
		}
	}
	mutation := action == "open" || action == "write"
	if mutation {
		if r.OperationID == "" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			r.OperationID = hex.EncodeToString(b)
		}
		if err := terminals.ValidateOperationID(r.OperationID); err != nil {
			return err
		}
	}
	if action == "write" {
		hasText, hasFile := false, false
		f.Visit(func(v *flag.Flag) {
			if v.Name == "text" {
				hasText = true
			}
			if v.Name == "file" {
				hasFile = true
			}
		})
		if hasText == hasFile {
			return errors.New("write requires exactly one of --text or --file")
		}
		if hasFile {
			var err error
			if file == "-" {
				if stdin == nil {
					return errors.New("stdin unavailable")
				}
				r.Data, err = io.ReadAll(io.LimitReader(stdin, terminals.MaxWrite+1))
			} else {
				r.Data, err = readRegular(file, terminals.MaxWrite)
			}
			if err != nil {
				return err
			}
		} else {
			r.Data = []byte(text)
		}
		if len(r.Data) == 0 {
			return errors.New("terminal input must be nonempty")
		}
		if len(r.Data) > terminals.MaxWrite {
			return errors.New("terminal input exceeds 32768 bytes")
		}
	}
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(retry.Context(parent), *timeout)
	defer cancel()
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	go func() { <-ctx.Done(); client.Close() }()
	if err = terminalsCapability(client); err != nil {
		return err
	}
	response, err := terminalsRequest(client, r)
	unknown := func(e error) error {
		if ctx.Err() != nil {
			e = ctx.Err()
		}
		return &TerminalOperationUnknownError{Action: action, OperationID: r.OperationID, TerminalID: r.ID, Err: e}
	}
	if err != nil {
		var rejected *terminalsRejectedError
		if mutation && !errors.As(err, &rejected) {
			return unknown(err)
		}
		return err
	}
	for mutation {
		if response.Terminal != nil && r.ID == "" {
			r.ID = response.Terminal.ID
		}
		if response.Operation == nil {
			return unknown(errors.New("missing terminal operation response"))
		}
		if response.Operation.Status == "completed" {
			break
		}
		if response.Operation.Status == "failed" {
			if *asJSON {
				if err = json.NewEncoder(stdout).Encode(response); err != nil {
					return err
				}
			}
			return fmt.Errorf("terminal operation failed: %s", response.Operation.Error)
		}
		if response.Operation.Status != "pending" && response.Operation.Status != "running" {
			return unknown(errors.New("invalid terminal operation status"))
		}
		select {
		case <-ctx.Done():
			return unknown(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
		query := terminals.Request{Version: 1, Action: "operation", OperationID: r.OperationID}
		if action == "write" {
			query.ID = r.ID
		}
		response, err = terminalsRequest(client, query)
		if err != nil {
			return unknown(err)
		}
	}
	// Byte data remains base64 encoded, including in the default bounded output.
	return json.NewEncoder(stdout).Encode(response)
}
func terminalsCapability(client *ssh.Client) error {
	ok, data, err := client.SendRequest("capabilities@shellpier", true, nil)
	if err != nil {
		return err
	}
	var cap struct {
		HostTerminalsProtocol int `json:"host_terminals_protocol"`
	}
	if !ok {
		return errors.New("unsupported host-owned terminals: upgrade the host")
	}
	if len(data) > 4096 || json.Unmarshal(data, &cap) != nil {
		return errors.New("invalid bounded capabilities response")
	}
	if cap.HostTerminalsProtocol != 1 {
		return errors.New("unsupported host-owned terminals: use a current host in full access mode")
	}
	return nil
}
func terminalsRequest(client *ssh.Client, r terminals.Request) (terminals.Response, error) {
	var out terminals.Response
	raw, err := json.Marshal(r)
	if err != nil {
		return out, err
	}
	if len(raw) > terminals.MaxPayload {
		return out, &terminalsRejectedError{"terminal request exceeds payload limit"}
	}
	ok, data, err := client.SendRequest("terminals@shellpier", true, raw)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, &terminalsRejectedError{"unsupported host-owned terminals"}
	}
	if len(data) > 256*1024 || json.Unmarshal(data, &out) != nil {
		return out, errors.New("invalid bounded terminal response")
	}
	if len(out.Terminals) > terminals.MaxTerminals || (out.Output != nil && len(out.Output.Data) > terminals.MaxRead) {
		return out, errors.New("terminal response exceeds protocol limits")
	}
	if !out.OK {
		return out, &terminalsRejectedError{out.Error}
	}
	return out, nil
}
