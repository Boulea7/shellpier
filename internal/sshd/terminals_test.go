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

	"github.com/Boulea7/shellpier/internal/terminals"
	"golang.org/x/crypto/ssh"
)

func terminalsEndpoint(t *testing.T, mode AccessMode) (func() *ssh.Client, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	host, user := signer(t), signer(t)
	options := terminals.Options{}
	options.WorkDir = t.TempDir()
	options.Shell = "sh"
	if runtime.GOOS == "windows" {
		options.Shell = "cmd"
	}
	var manager *terminals.Manager
	var err error
	manager, err = terminals.New(ctx, options)
	if err != nil {
		t.Fatal(err)
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
				(&Server{Signer: host, AuthorizedKey: user.PublicKey(), Shell: options.Shell, WorkDir: options.WorkDir, Terminals: manager, AccessMode: mode}).ServeConn(ctx, conn)
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
func terminalsRPC(t *testing.T, c *ssh.Client, request terminals.Request) terminals.Response {
	t.Helper()
	request.Version = 1
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ok, data, err := c.SendRequest(TerminalsRequest, true, payload)
	if err != nil || !ok {
		t.Fatalf("jobs request failed: %v", err)
	}
	var response terminals.Response
	if json.Unmarshal(data, &response) != nil {
		t.Fatal("invalid jobs reply")
	}
	return response
}
func TestHostTerminalReconnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell integration")
	}
	dial, _ := terminalsEndpoint(t, AccessFull)
	first := dial()
	call := func(c *ssh.Client, r terminals.Request) terminals.Response {
		out := terminalsRPC(t, c, r)
		if !out.OK {
			t.Fatalf("RPC failed: %s", out.Error)
		}
		return out
	}
	wait := func(f func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if f() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("terminal wait exceeded")
	}
	opened := call(first, terminals.Request{Action: "open", OperationID: "reconnect"})
	id := opened.Terminal.ID
	wait(func() bool {
		return call(first, terminals.Request{Action: "status", ID: id}).Terminal.State == "running"
	})
	call(first, terminals.Request{Action: "write", ID: id, OperationID: "setup", Data: []byte("SHELLPIER_RECONNECT=kept; cd /; printf '__SETUP__\\n'\n")})
	wait(func() bool {
		return call(first, terminals.Request{Action: "operation", ID: id, OperationID: "setup"}).Operation.Status == "completed"
	})
	first.Close()
	second := dial()
	if call(second, terminals.Request{Action: "open", OperationID: "reconnect"}).Terminal.ID != id {
		t.Fatal("open replay created another PTY")
	}
	call(second, terminals.Request{Action: "write", ID: id, OperationID: "verify", Data: []byte("printf '__RESTORED_%s_%s__\\n' \"$SHELLPIER_RECONNECT\" \"$PWD\"\n")})
	wait(func() bool {
		return strings.Contains(string(call(second, terminals.Request{Action: "read", ID: id}).Output.Data), "__RESTORED_kept_/__")
	})
	call(second, terminals.Request{Action: "close", ID: id})
	wait(func() bool {
		return call(second, terminals.Request{Action: "status", ID: id}).Terminal.State == "closed"
	})
}
func TestHostTerminalAccessModes(t *testing.T) {
	for _, mode := range []AccessMode{AccessSFTP, AccessSFTPReadOnly} {
		t.Run(string(mode), func(t *testing.T) {
			dial, _ := terminalsEndpoint(t, mode)
			client := dial()
			payload, _ := json.Marshal(terminals.Request{Version: 1, Action: "open", OperationID: "forbidden"})
			ok, _, err := client.SendRequest(TerminalsRequest, true, payload)
			if err != nil || ok {
				t.Fatalf("access mode did not refuse PTY: %v", err)
			}
			ok, data, err := client.SendRequest("capabilities@shellpier", true, nil)
			if err != nil || !ok {
				t.Fatal(err)
			}
			var cap capabilities
			if json.Unmarshal(data, &cap) != nil || cap.HostTerminalsProtocol != 0 {
				t.Fatal("advertised host PTY outside full mode")
			}
		})
	}
}
