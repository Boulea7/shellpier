package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/terminals"
	"golang.org/x/crypto/ssh"
)

func terminalsCLIEndpoint(t *testing.T, protocol int, handler func(terminals.Request) (terminals.Response, bool)) string {
	t.Helper()
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
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), pub.Marshal()) {
			return nil, errors.New("rejected")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer c.Close()
				sc, ch, requests, e := ssh.NewServerConn(c, config)
				if e != nil {
					return
				}
				defer sc.Close()
				go func() {
					for v := range ch {
						v.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}()
				for r := range requests {
					if r.Type == "capabilities@shellpier" {
						b, _ := json.Marshal(map[string]int{"host_terminals_protocol": protocol})
						r.Reply(true, b)
						continue
					}
					if r.Type != "terminals@shellpier" {
						r.Reply(false, nil)
						continue
					}
					var q terminals.Request
					if json.Unmarshal(r.Payload, &q) != nil {
						return
					}
					out, lose := handler(q)
					if lose {
						return
					}
					b, _ := json.Marshal(out)
					r.Reply(true, b)
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done; workers.Wait() })
	a.Addresses = []string{ln.Addr().String()}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return "@" + path
}
func TestTerminalsValidationAndHelp(t *testing.T) {
	for _, args := range [][]string{{"open", "@missing", "--help"}, {"write", "@missing", "x", "--file", "@missing", "--help"}} {
		if err := terminalsCommand(context.Background(), args, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "help requested") {
			t.Fatalf("help: %v", err)
		}
	}
	for _, args := range [][]string{{"status", "@missing", "x", "--text", "bad"}, {"open", "@missing", "--lifetime", "25h"}, {"read", "@missing", "x", "--limit", "32769"}, {"write", "@missing", "x", "--text", "a", "--file", "x"}, {"operation", "@missing", "bad/id"}, {"resize", "@missing", "x", "--rows", "0"}} {
		if err := terminalsCommand(context.Background(), args, nil, io.Discard, io.Discard); err == nil || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("validation %+v: %v", args, err)
		}
	}
}
func TestTerminalsUnsupported(t *testing.T) {
	ticket := terminalsCLIEndpoint(t, 0, func(terminals.Request) (terminals.Response, bool) {
		t.Error("mutation sent to old host")
		return terminals.Response{}, false
	})
	err := terminalsCommand(context.Background(), []string{"open", ticket}, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsupported host-owned") {
		t.Fatalf("unsupported: %v", err)
	}
	var unknown *TerminalOperationUnknownError
	if errors.As(err, &unknown) {
		t.Fatal("unsupported is not an unknown mutation")
	}
}
func TestTerminalsMutationPollingAndUnknown(t *testing.T) {
	for _, action := range []string{"open", "write"} {
		t.Run(action, func(t *testing.T) {
			var mu sync.Mutex
			mutations, queries := 0, 0
			ticket := terminalsCLIEndpoint(t, 1, func(r terminals.Request) (terminals.Response, bool) {
				mu.Lock()
				defer mu.Unlock()
				if r.Version != 1 {
					t.Error("missing protocol version")
				}
				if r.Action == action {
					mutations++
					return terminals.Response{OK: true, Terminal: &terminals.Terminal{ID: "terminal1"}, Operation: &terminals.Operation{OperationID: "stable", Status: "pending"}}, false
				}
				queries++
				if r.Action != "operation" || r.OperationID != "stable" || (action == "write" && r.ID != "terminal1") || (action == "open" && r.ID != "") {
					t.Errorf("query: %+v", r)
				}
				return terminals.Response{OK: true, Operation: &terminals.Operation{OperationID: "stable", Status: "completed", TerminalID: "terminal1", BytesWritten: 3}}, false
			})
			args := []string{action, ticket}
			if action == "write" {
				args = append(args, "terminal1", "--text", "abc")
			}
			args = append(args, "--operation-id", "stable", "--json")
			var out bytes.Buffer
			if err := terminalsCommand(context.Background(), args, nil, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if mutations != 1 || queries != 1 {
				t.Errorf("mutations=%d queries=%d", mutations, queries)
			}
			mu.Unlock()
			if !strings.Contains(out.String(), "completed") {
				t.Fatal(out.String())
			}
			lost := terminalsCLIEndpoint(t, 1, func(terminals.Request) (terminals.Response, bool) { return terminals.Response{}, true })
			args[1] = lost
			err := terminalsCommand(context.Background(), args, nil, io.Discard, io.Discard)
			var unknown *TerminalOperationUnknownError
			if !errors.As(err, &unknown) || unknown.Action != action || unknown.OperationID != "stable" || (action == "write" && unknown.TerminalID != "terminal1") {
				t.Fatalf("unknown: %v", err)
			}
		})
	}
}

func TestTerminalsReconnectSameHostTerminal(t *testing.T) {
	if !platform.TerminalSupported() {
		t.Skip("PTY unavailable")
	}
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "cmd"
	}
	manager, err := terminals.New(context.Background(), terminals.Options{Shell: shell, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ticket := terminalsCLIEndpoint(t, 1, func(r terminals.Request) (terminals.Response, bool) { return manager.Do(r), false })
	call := func(args ...string) terminals.Response {
		t.Helper()
		var out bytes.Buffer
		if err := terminalsCommand(context.Background(), append(args, "--json"), nil, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var r terminals.Response
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	opened := call("open", ticket, "--operation-id", "open-reconnect")
	id := opened.Operation.TerminalID
	if id == "" {
		t.Fatal("missing terminal ID")
	}
	setVariable, readVariable := "SP_RECONNECT=retained_state\n", "printf 'SAVED=%s\\n' \"$SP_RECONNECT\"\n"
	if runtime.GOOS == "windows" {
		setVariable, readVariable = "set SP_RECONNECT=retained_state\r\n", "echo SAVED=%SP_RECONNECT%\r\n"
	}
	call("write", ticket, id, "--text", setVariable, "--operation-id", "set-reconnect")
	call("write", ticket, id, "--text", readVariable, "--operation-id", "read-reconnect")
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	var cursor uint64
	for time.Now().Before(deadline) {
		r := call("read", ticket, id, "--cursor", strconv.FormatUint(cursor, 10))
		if r.Output == nil {
			t.Fatal("missing output")
		}
		data = append(data, r.Output.Data...)
		cursor = r.Output.NextCursor
		if bytes.Contains(data, []byte("SAVED=retained_state")) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Contains(data, []byte("SAVED=retained_state")) {
		t.Fatal("terminal shell state unavailable after reconnect")
	}
	status := call("status", ticket, id)
	if status.Terminal == nil || status.Terminal.ID != id {
		t.Fatal("terminal changed across connections")
	}
	call("resize", ticket, id, "--cols", "100", "--rows", "30")
	call("close", ticket, id)
}

func TestTerminalsUnknownQueryNamespace(t *testing.T) {
	for _, action := range []string{"open", "write"} {
		err := &TerminalOperationUnknownError{Action: action, OperationID: "stable", TerminalID: "terminal1", Err: io.EOF}
		query := "terminals operation TICKET stable"
		if action == "write" {
			query += " --id terminal1"
		}
		if !strings.Contains(err.Error(), query) || (action == "open" && strings.Contains(err.Error(), "--id")) {
			t.Fatalf("wrong query: %s", err)
		}
	}
}
