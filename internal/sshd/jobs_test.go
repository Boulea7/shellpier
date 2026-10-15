package sshd

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/jobs"
	"golang.org/x/crypto/ssh"
)

func jobsEndpoint(t *testing.T, enabled bool) (func() *ssh.Client, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	host, user := signer(t), signer(t)
	options := jobs.DefaultOptions()
	options.WorkDir = t.TempDir()
	options.Shell = "sh"
	if runtime.GOOS == "windows" {
		options.Shell = "cmd"
	}
	var manager *jobs.Manager
	var err error
	if enabled {
		manager, err = jobs.New(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	accepted := make(chan struct{})
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
				(&Server{Signer: host, AuthorizedKey: user.PublicKey(), Shell: options.Shell, WorkDir: options.WorkDir, Jobs: manager}).ServeConn(ctx, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		<-accepted
		workers.Wait()
		if manager != nil {
			if err := manager.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return func() *ssh.Client {
		t.Helper()
		client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{User: "shellpier", Auth: []ssh.AuthMethod{ssh.PublicKeys(user)}, HostKeyCallback: ssh.FixedHostKey(host.PublicKey()), Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		return client
	}, cancel
}
func jobsRPC(t *testing.T, c *ssh.Client, request jobs.Request) jobs.Response {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ok, data, err := c.SendRequest(JobsRequest, true, payload)
	if err != nil || !ok {
		t.Fatalf("jobs request failed: %v", err)
	}
	var response jobs.Response
	if json.Unmarshal(data, &response) != nil {
		t.Fatal("invalid jobs reply")
	}
	return response
}
func TestJobsReconnectWithoutCommandReplay(t *testing.T) {
	dial, _ := jobsEndpoint(t, true)
	first := dial()
	command := "printf started; sleep 0.2; printf done; printf error >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "echo started & ping -n 2 127.0.0.1 >nul & echo done & echo error 1>&2 & exit /b 7"
	}
	request := jobs.Request{Action: "start", OperationID: "reconnect-operation", Command: command}
	started := jobsRPC(t, first, request)
	if !started.OK {
		t.Fatal(started.Error)
	}
	first.Close()
	second := dial()
	duplicate := jobsRPC(t, second, request)
	if !duplicate.OK || duplicate.Job.ID != started.Job.ID {
		t.Fatal("reconnect repeated the start instead of reusing the job")
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		status := jobsRPC(t, second, jobs.Request{Action: "status", ID: started.Job.ID})
		if status.Job.State != "running" {
			if status.Job.ExitCode == nil || *status.Job.ExitCode != 7 {
				t.Fatal("job exit7 did not survive SSH reconnection")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job stopped progressing after the original SSH connection closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs := jobsRPC(t, second, jobs.Request{Action: "logs", ID: started.Job.ID})
	if !logs.OK || !strings.Contains(string(logs.Stdout.Data), "done") || !strings.Contains(string(logs.Stderr.Data), "error") {
		t.Fatal("reconnected logs were lost")
	}
}
func TestJobsAreExplicitlyDisabled(t *testing.T) {
	dial, _ := jobsEndpoint(t, false)
	c := dial()
	ok, _, err := c.SendRequest(JobsRequest, true, []byte(`{"action":"list"}`))
	if err != nil || ok {
		t.Fatal("jobs were available without opt-in")
	}
}
