package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	jobstate "github.com/Boulea7/shellpier/internal/jobs"
	"golang.org/x/crypto/ssh"
)

func jobsCommand(parent context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier jobs start|status|operation|list|logs|wait|cancel TICKET [JOB-ID or OPERATION-ID] [flags]\nThe host must enable --allow-jobs. Jobs survive SSH disconnects but end with the host session. Preserve the operation ID when a start response is lost.")
		return err
	}
	action := args[0]
	switch action {
	case "start", "status", "operation", "list", "logs", "wait", "cancel":
	default:
		return errors.New("unknown jobs action")
	}
	f := flags("jobs "+action, stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	asJSON := f.Bool("json", false, "emit one structured job response")
	defaultTimeout := 20 * time.Second
	if action == "start" || action == "wait" {
		defaultTimeout = 3 * time.Hour
	}
	timeout := f.Duration("timeout", defaultTimeout, "job runtime for start, total client timeout otherwise")
	var command, script, cwd, operation string
	var env environmentValues
	var stdoutCursor, stderrCursor int64
	var limit int
	if action == "start" {
		f.StringVar(&command, "command", "", "exact remote shell command")
		f.StringVar(&script, "script", "", "local UTF-8 command script")
		f.StringVar(&cwd, "cwd", "", "remote working directory")
		f.StringVar(&operation, "operation-id", "", "stable ID for safe start deduplication")
		f.Var(&env, "env", "remote NAME=VALUE; repeatable")
	}
	if action == "logs" {
		f.Int64Var(&stdoutCursor, "stdout-cursor", 0, "absolute stdout byte offset")
		f.Int64Var(&stderrCursor, "stderr-cursor", 0, "absolute stderr byte offset")
		f.IntVar(&limit, "limit", jobstate.MaxRead, "maximum bytes per output stream")
	}
	local, remote, separated := splitCommandArgs(f, args[1:])
	if err := parseFlags(f, local); err != nil {
		return err
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	parent = retry.Context(parent)
	if *timeout < time.Millisecond || *timeout > 7*24*time.Hour {
		return errors.New("timeout must be between 1ms and 168h")
	}
	expected := 2
	if action == "start" || action == "list" {
		expected = 1
	}
	if f.NArg() != expected {
		return errors.New("provide an access ticket and, except start/list, exactly one job ID")
	}
	request := jobstate.Request{Action: action, StdoutCursor: stdoutCursor, StderrCursor: stderrCursor, Limit: limit}
	if action == "start" {
		if !separated {
			remote = f.Args()[1:]
		}
		var err error
		request.Command, err = remoteCommand("jobs start", command, script, remote)
		if err != nil {
			return err
		}
		if operation == "" {
			data := make([]byte, 16)
			if _, err = rand.Read(data); err != nil {
				return err
			}
			operation = hex.EncodeToString(data)
		}
		if err := jobstate.ValidateOperationID(operation); err != nil {
			return err
		}
		request.OperationID = operation
		request.CWD = cwd
		request.Env = env
		request.TimeoutMS = timeout.Milliseconds()
	} else {
		if separated {
			return errors.New("only jobs start accepts a remote command")
		}
		if action == "operation" {
			if err := jobstate.ValidateOperationID(f.Arg(1)); err != nil {
				return err
			}
			request.Action = "status"
			request.OperationID = f.Arg(1)
		} else if action != "list" {
			request.ID = f.Arg(1)
		}
	}
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	clientTimeout := *timeout
	if action == "start" {
		clientTimeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, clientTimeout)
	defer cancel()
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	if action == "start" {
		if _, err = fmt.Fprintf(stderr, "Operation ID: %s; retain it to query or retry an uncertain start.\n", operation); err != nil {
			return err
		}
	}
	if action == "wait" {
		request.Action = "status"
	}
	var collected []jobstate.Job
	for {
		response, err := jobRequest(client, request)
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if action == "start" {
				var rejected *jobRejectedError
				if errors.As(err, &rejected) {
					return fmt.Errorf("job start rejected (operation-id %s): %w", operation, err)
				}
				return &JobStartUnknownError{OperationID: operation, Err: err}
			}
			return err
		}
		if action == "list" {
			if len(response.Jobs) > 128 || len(collected)+len(response.Jobs) > 1024 {
				return errors.New("job list exceeds protocol limits")
			}
			collected = append(collected, response.Jobs...)
			if response.NextCursor != "" {
				if response.NextCursor <= request.After {
					return errors.New("job list cursor did not advance")
				}
				request.After = response.NextCursor
				continue
			}
			response.Jobs = collected
		}
		if action != "wait" || response.Job == nil || response.Job.State != "running" {
			var outcome error
			if action == "wait" && response.Job != nil && response.Job.State != "succeeded" {
				response.OK = false
				outcome = &JobExitError{State: response.Job.State, Code: response.Job.ExitCode}
			}
			if *asJSON {
				if err = json.NewEncoder(stdout).Encode(response); err != nil {
					return errors.Join(outcome, err)
				}
				return outcome
			}
			if action == "logs" {
				if response.Stdout != nil {
					if _, err = stdout.Write(response.Stdout.Data); err != nil {
						return err
					}
				}
				if response.Stderr != nil {
					if _, err = stderr.Write(response.Stderr.Data); err != nil {
						return err
					}
				}
				return nil
			}
			if response.Job != nil {
				_, err = fmt.Fprintf(stdout, "%s %s", response.Job.ID, response.Job.State)
				if response.Job.ExitCode != nil && err == nil {
					_, err = fmt.Fprintf(stdout, " exit=%d", *response.Job.ExitCode)
				}
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(stdout)
				return errors.Join(outcome, err)
			}
			for _, value := range response.Jobs {
				if _, err = fmt.Fprintf(stdout, "%s %s\n", value.ID, value.State); err != nil {
					return err
				}
			}
			return nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func jobRequest(client *ssh.Client, request jobstate.Request) (jobstate.Response, error) {
	var response jobstate.Response
	payload, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	if len(payload) > jobstate.MaxPayload {
		return response, &jobRejectedError{"encoded job request exceeds 96 KiB; reduce command/environment size"}
	}
	ok, data, err := client.SendRequest("jobs@shellpier", true, payload)
	if err != nil {
		return response, err
	}
	if !ok {
		return response, &jobRejectedError{"target rejected jobs; enable --allow-jobs on the host"}
	}
	if len(data) > 256*1024 || json.Unmarshal(data, &response) != nil {
		return response, errors.New("invalid bounded job response")
	}
	if !response.OK {
		return response, &jobRejectedError{response.Error}
	}
	return response, nil
}

type jobRejectedError struct{ message string }

func (e *jobRejectedError) Error() string { return e.message }

// JobStartUnknownError retains the idempotency key when a start response is lost.
type JobStartUnknownError struct {
	OperationID string
	Err         error
}

func (e *JobStartUnknownError) Error() string {
	return fmt.Sprintf("job start result unknown; preserve operation-id %s and retry the same content: %v", e.OperationID, e.Err)
}
func (e *JobStartUnknownError) Unwrap() error { return e.Err }

type JobExitError struct {
	State string
	Code  *uint32
}

func (e *JobExitError) Error() string {
	return fmt.Sprintf("remote job finished with state %s and exit code %d", e.State, e.ExitCode())
}
func (e *JobExitError) ExitCode() int {
	if e.State == "timed_out" {
		return 124
	}
	if e.State == "cancelled" {
		return 130
	}
	if e.Code != nil && *e.Code > 0 && *e.Code < 256 {
		return int(*e.Code)
	}
	return 1
}
