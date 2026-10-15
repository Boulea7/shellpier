package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/relay"
)

// ReportedError preserves the cause after a diagnostic has been emitted.
type ReportedError struct{ Err error }

func (e *ReportedError) Error() string {
	if e.Err == nil {
		return "reported error"
	}
	return e.Err.Error()
}
func (e *ReportedError) Unwrap() error { return e.Err }

// UsageError marks a rejected CLI request without exposing its input in JSON.
type UsageError struct{ Err error }

func (e *UsageError) Error() string {
	if e.Err == nil {
		return "invalid usage"
	}
	return e.Err.Error()
}
func (e *UsageError) Unwrap() error { return e.Err }

// ExitCode uses the same error categories as the machine-readable diagnostic.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var jobExit *JobExitError
	if errors.As(err, &jobExit) {
		return jobExit.ExitCode()
	}
	detail := classifyJSONError(err)
	switch detail.Code {
	case "usage":
		return 2
	case "canceled":
		return 130
	case "timeout":
		return 124
	case "remote_exit":
		if detail.RemoteExitCode != nil && *detail.RemoteExitCode > 0 && *detail.RemoteExitCode < 256 {
			return *detail.RemoteExitCode
		}
	}
	return 1
}

type jsonErrorDetail struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	RemoteExitCode *int   `json:"remote_exit_code,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
}

func classifyJSONError(err error) jsonErrorDetail {
	result := jsonErrorDetail{Code: "internal", Message: "Operation failed; inspect local diagnostics without sharing credentials."}
	var remote interface{ ExitStatus() int }
	var network net.Error
	var usage *UsageError
	var unknown *JobStartUnknownError
	var jobExit *JobExitError
	text := strings.ToLower(err.Error())
	switch {
	case errors.As(err, &unknown):
		result.Code = "outcome_unknown"
		result.Message = "Job submission could not be confirmed. Query this operation ID before retrying."
		result.OperationID, result.Outcome = unknown.OperationID, "unknown"
	case errors.As(err, &jobExit):
		code := jobExit.ExitCode()
		result.Code, result.Message = "remote_exit", "Remote job did not succeed; inspect the job result."
		result.RemoteExitCode = &code
	case errors.Is(err, context.Canceled):
		result.Code = "canceled"
		result.Message = "Operation was canceled."
	case errors.Is(err, context.DeadlineExceeded):
		result.Code = "timeout"
		result.Message = "Operation timed out; check remote effects before retrying."
	case errors.As(err, &remote):
		code := remote.ExitStatus()
		result.Code = "remote_exit"
		result.Message = "Remote command returned a nonzero exit status."
		result.RemoteExitCode = &code
	case errors.Is(err, access.ErrKeepaliveTimeout):
		result.Code = "connection_lost"
		result.Message = "SSH peer stopped responding. Remote effects may be unknown; inspect state before retrying."
	case errors.Is(err, os.ErrPermission):
		result.Code = "permission"
		result.Message = "Access was denied."
	case errors.Is(err, os.ErrNotExist):
		result.Code = "not_found"
		result.Message = "Required file or resource was not found."
	case errors.Is(err, platform.ErrTerminalUnsupported) || errors.Is(err, syscall.ENOSYS) || strings.Contains(text, "not supported") || strings.HasPrefix(text, "unsupported "):
		result.Code = "unsupported"
		result.Message = "This operation is not supported by the selected platform or interface."
	case errors.Is(err, relay.ErrUnauthorized) || strings.Contains(text, "authentication") || strings.Contains(text, "host key mismatch") || strings.Contains(text, "host key does not match") || strings.Contains(text, "host-key") || strings.Contains(text, "unable to authenticate"):
		result.Code = "authentication"
		result.Message = "Authentication or host-key verification failed."
	case errors.As(err, &usage) || errors.Is(err, relay.ErrInvalidURL) || strings.HasPrefix(text, "usage:") || strings.HasPrefix(text, "unknown ") || strings.HasPrefix(text, "invalid ") || strings.Contains(text, "flag provided but not defined") || strings.Contains(text, "flag needs an argument") || strings.HasPrefix(text, "provide ") || strings.Contains(text, "accepts flags only") || strings.HasPrefix(text, "requires ") || strings.HasPrefix(text, "expected ") || strings.Contains(text, " must be ") || strings.Contains(text, " requires "):
		result.Code = "usage"
		result.Message = "Invalid command arguments; consult command help or schema."
	case errors.As(err, &network):
		if network.Timeout() {
			result.Code = "timeout"
			result.Message = "Connection timed out; remote effects may require inspection."
		} else {
			result.Code = "connection"
			result.Message = "Network connection failed."
		}
	case errors.Is(err, relay.ErrUnavailable) || errors.Is(err, relay.ErrSessionClosed) || errors.Is(err, relay.ErrHandshakeRejected) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(text, "connection failed") || strings.Contains(text, "not reachable"):
		result.Code = "connection"
		result.Message = "Connection failed or the session is no longer available."
	}
	return result
}

// WriteJSONError emits one credential-free diagnostic and preserves the cause.
// Nil input emits nothing. Writer failures are returned rather than suppressed.
func WriteJSONError(stderr io.Writer, command string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := findCommandSpec(command); !ok {
		command = "unknown"
	}
	payload := struct {
		SchemaVersion int             `json:"schema_version"`
		Command       string          `json:"command"`
		OK            bool            `json:"ok"`
		Error         jsonErrorDetail `json:"error"`
	}{1, command, false, classifyJSONError(err)}
	if writeErr := json.NewEncoder(stderr).Encode(payload); writeErr != nil {
		return writeErr
	}
	return &ReportedError{Err: err}
}

// JSONRequested recognizes only local flags, never flag-looking remote values.
// Call with the complete CLI argument list (including the command name).
func JSONRequested(args []string) bool {
	requested := false
	var selected commandSpec
	found := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if !found && !strings.HasPrefix(arg, "-") {
			selected, found = findCommandSpec(arg)
			continue
		}
		if found && len(selected.Subcommands) > 0 && !strings.HasPrefix(arg, "-") {
			for _, sub := range selected.Subcommands {
				if sub.Name == arg {
					selected = sub
					break
				}
			}
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name == "json" {
			if !hasValue {
				requested = true
			} else {
				switch value {
				case "true", "1", "t", "TRUE", "True":
					requested = true
				case "false", "0", "f", "FALSE", "False":
					requested = false
				}
			}
			continue
		}
		if hasValue {
			continue
		}
		for _, flag := range selected.Flags {
			if flag.Name == name && flag.Type != "bool" {
				index++
				break
			}
		}
	}
	return requested
}
