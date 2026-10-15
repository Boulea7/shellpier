package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/relay"
)

type testRemoteExit int

func (e testRemoteExit) Error() string   { return "secret remote command" }
func (e testRemoteExit) ExitStatus() int { return int(e) }
func TestJSONErrorCategoriesAndCause(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{{&UsageError{errors.New("private arguments")}, "usage"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}, {testRemoteExit(42), "remote_exit"}, {os.ErrPermission, "permission"}, {os.ErrNotExist, "not_found"}, {&net.OpError{Op: "dial", Err: errors.New("private address")}, "connection"}, {errors.Join(access.ErrKeepaliveTimeout, io.EOF), "connection_lost"}, {relay.ErrUnauthorized, "authentication"}, {platform.ErrTerminalUnsupported, "unsupported"}, {errors.New("private internal detail"), "internal"}}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			cause := fmt.Errorf("sensitive wrapper: %w", tc.err)
			var out bytes.Buffer
			err := WriteJSONError(&out, "exec", cause)
			var reported *ReportedError
			if !errors.As(err, &reported) || !errors.Is(err, tc.err) {
				t.Fatal("cause was not preserved")
			}
			var payload struct {
				SchemaVersion int             `json:"schema_version"`
				Command       string          `json:"command"`
				OK            bool            `json:"ok"`
				Error         jsonErrorDetail `json:"error"`
			}
			if e := json.Unmarshal(out.Bytes(), &payload); e != nil {
				t.Fatal(e)
			}
			if payload.SchemaVersion != 1 || payload.Command != "exec" || payload.OK || payload.Error.Code != tc.code {
				t.Fatalf("bad payload: %+v", payload)
			}
			if tc.code == "remote_exit" && (payload.Error.RemoteExitCode == nil || *payload.Error.RemoteExitCode != 42) {
				t.Fatal("remote status lost")
			}
			if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "sensitive") {
				t.Fatal("raw error escaped")
			}
		})
	}
}
func TestJSONErrorNeverCopiesSecrets(t *testing.T) {
	secret := "sp1_ticket-secret -----BEGIN PRIVATE KEY----- https://user:password@relay.invalid"
	var out bytes.Buffer
	WriteJSONError(&out, secret, errors.New(secret))
	if strings.Contains(out.String(), "password") || strings.Contains(out.String(), "sp1_") || strings.Contains(out.String(), "PRIVATE KEY") || !strings.Contains(out.String(), `"command":"unknown"`) {
		t.Fatal("secret copied into JSON")
	}
}
func TestJSONErrorWriterAndNil(t *testing.T) {
	var out bytes.Buffer
	if err := WriteJSONError(&out, "exec", nil); err != nil || out.Len() != 0 {
		t.Fatal("nil error was emitted")
	}
	failure := errors.New("writer failed")
	if err := WriteJSONError(completionFailWriter{failure}, "exec", io.EOF); !errors.Is(err, failure) {
		t.Fatal("writer failure lost")
	}
}

func TestJSONRequestedOnlyLocalOptions(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--json", "exec", "@file", "--", "echo", "--json=false"}, true},
		{[]string{"exec", "--command", "echo --json", "@file"}, false},
		{[]string{"exec", "--command", "--json", "@file"}, false},
		{[]string{"exec", "@file", "--json=true"}, true},
		{[]string{"exec", "--json", "--json=false", "@file"}, false},
		{[]string{"exec", "@file", "--", "--json"}, false},
		{[]string{"files", "read", "--json", "@file", "path"}, true},
		{[]string{"shell", "--env", "VALUE=--json", "@file"}, false},
		{[]string{"terminal", "write", "--text", "--json", "--state", "file"}, false},
		{[]string{"terminal", "write", "--text", "--json", "--state", "file", "--json"}, true},
	}
	for _, tc := range cases {
		if got := JSONRequested(tc.args); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.args, got, tc.want)
		}
	}
}

func TestUnknownJobSubmissionJSONPreservesIdentityAndCause(t *testing.T) {
	cause := &JobStartUnknownError{OperationID: "build-42", Err: context.DeadlineExceeded}
	var diagnostics bytes.Buffer
	err := WriteJSONError(&diagnostics, "jobs", cause)
	if !errors.Is(err, context.DeadlineExceeded) || ExitCode(err) != 1 {
		t.Fatal("unknown submission must preserve the cause and return an unknown-outcome failure")
	}
	var result struct {
		Error jsonErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(diagnostics.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != "outcome_unknown" || result.Error.OperationID != "build-42" || result.Error.Outcome != "unknown" {
		t.Fatalf("lost retry identity: %+v", result.Error)
	}
	code := uint32(7)
	if ExitCode(&ReportedError{Err: &JobExitError{State: "failed", Code: &code}}) != 7 {
		t.Fatal("job failure lost its exit code")
	}
}

func TestKeepaliveErrorPreservesUnknownJobOutcome(t *testing.T) {
	err := &JobStartUnknownError{OperationID: "build-keepalive", Err: access.ErrKeepaliveTimeout}
	result := classifyJSONError(err)
	if result.Code != "outcome_unknown" || result.OperationID != "build-keepalive" || result.Outcome != "unknown" {
		t.Fatalf("lost job submission uncertainty: %+v", result)
	}
	if ExitCode(err) != 1 {
		t.Fatal("connection loss must not resemble a confirmed remote exit")
	}
}
