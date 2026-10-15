package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	jobstate "github.com/Boulea7/shellpier/internal/jobs"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func jobCLIEndpoint(t *testing.T, loseStart bool) (string, *jobstate.Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, h, err := access.NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
	if err != nil {
		t.Fatal(err)
	}
	opts := jobstate.DefaultOptions()
	opts.WorkDir = t.TempDir()
	opts.Shell = "sh"
	if runtime.GOOS == "windows" {
		opts.Shell = "cmd"
	}
	manager, err := jobstate.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.Addresses = []string{ln.Addr().String()}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "access.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(accepted)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				if !loseStart {
					(&sshd.Server{Signer: host, AuthorizedKey: pub, Shell: opts.Shell, Jobs: manager}).ServeConn(ctx, conn)
					return
				}
				defer conn.Close()
				config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
					if !bytes.Equal(key.Marshal(), pub.Marshal()) {
						return nil, errors.New("rejected")
					}
					return nil, nil
				}}
				config.AddHostKey(host)
				sc, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sc.Close()
				go func() {
					for ch := range channels {
						ch.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}()
				for request := range requests {
					if request.Type != sshd.JobsRequest {
						request.Reply(false, nil)
						continue
					}
					var payload jobstate.Request
					json.Unmarshal(request.Payload, &payload)
					response, err := manager.Handle(request.Payload)
					if payload.Action == "start" {
						sc.Close()
						return
					}
					request.Reply(err == nil, response)
				}
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		<-accepted
		workers.Wait()
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return "@" + file, manager
}
func TestJobsCLIStartWaitLogs(t *testing.T) {
	ticket, _ := jobCLIEndpoint(t, false)
	command := "printf stdout; printf stderr >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "echo stdout & echo stderr 1>&2 & exit /b 7"
	}
	var out, diagnostic bytes.Buffer
	err := jobsCommand(context.Background(), []string{"start", ticket, "--operation-id", "cli-id", "--command", command, "--json"}, &out, &diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	var started jobstate.Response
	if json.Unmarshal(out.Bytes(), &started) != nil || !started.OK || started.Job.OperationID != "cli-id" {
		t.Fatal("start metadata missing stable operation ID")
	}
	out.Reset()
	diagnostic.Reset()
	err = jobsCommand(context.Background(), []string{"wait", ticket, started.Job.ID, "--json"}, &out, &diagnostic)
	var exit *JobExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatal("wait did not expose job exit7")
	}
	var final jobstate.Response
	if json.Unmarshal(out.Bytes(), &final) != nil || final.Job.State != "failed" || final.OK {
		t.Fatal("wait snapshot missing failed state")
	}
	out.Reset()
	err = jobsCommand(context.Background(), []string{"logs", ticket, started.Job.ID, "--json"}, &out, &diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	var logs jobstate.Response
	if json.Unmarshal(out.Bytes(), &logs) != nil || !bytes.Contains(logs.Stdout.Data, []byte("stdout")) || !bytes.Contains(logs.Stderr.Data, []byte("stderr")) {
		t.Fatal("CLI log streams missing")
	}
}
func TestJobsCLIStartUnknownRetainsOperationID(t *testing.T) {
	ticket, manager := jobCLIEndpoint(t, true)
	var out, diagnostic bytes.Buffer
	err := jobsCommand(context.Background(), []string{"start", ticket, "--operation-id", "known-before-send", "--command", "echo once", "--json"}, &out, &diagnostic)
	var unknown *JobStartUnknownError
	if !errors.As(err, &unknown) || unknown.OperationID != "known-before-send" {
		t.Fatal("lost response did not preserve the start operation ID")
	}
	result := manager.Do(jobstate.Request{Action: "list"})
	if len(result.Jobs) != 1 || result.Jobs[0].OperationID != unknown.OperationID {
		t.Fatal("unknown start did not refer to the actual accepted task")
	}
	out.Reset()
	if err := jobsCommand(context.Background(), []string{"operation", ticket, unknown.OperationID, "--json"}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	var recovered jobstate.Response
	if json.Unmarshal(out.Bytes(), &recovered) != nil || !recovered.OK || recovered.Job == nil || recovered.Job.ID != result.Jobs[0].ID {
		t.Fatal("operation lookup did not recover the accepted task over a new SSH connection")
	}

}
func TestJobsCLINoSideEffectsOnHelpOrInvalidInput(t *testing.T) {
	for _, action := range []string{"start", "status", "operation", "list", "logs", "wait", "cancel"} {
		if err := jobsCommand(context.Background(), []string{action, "--help"}, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, flag.ErrHelp) {
			t.Fatal("jobs help did not precede networking")
		}
	}
	for _, args := range [][]string{{"start", "missing", "--operation-id", "bad\nID", "--command", "echo no"}, {"start", "missing", "--timeout", "1ns", "--command", "echo no"}, {"status", "missing"}, {"unknown"}} {
		if err := jobsCommand(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid jobs input accepted")
		}
	}
}

func TestJobsCLIEncodedStartTooLargeNotSent(t *testing.T) {
	ticket, manager := jobCLIEndpoint(t, false)
	var out, diagnostic bytes.Buffer
	// A valid-size shell command can grow beyond the wire limit after JSON escaping.
	command := strings.Repeat("\x01", 32*1024)
	err := jobsCommand(context.Background(), []string{"start", ticket, "--operation-id", "too-large", "--command", command, "--json"}, &out, &diagnostic)
	var unknown *JobStartUnknownError
	if err == nil || errors.As(err, &unknown) || !strings.Contains(err.Error(), "96 KiB") {
		t.Fatal("oversized serialized start was not rejected as known-unsent")
	}
	response := manager.Do(jobstate.Request{Action: "list"})
	if len(response.Jobs) != 0 {
		t.Fatal("oversized start reached target manager")
	}
}
