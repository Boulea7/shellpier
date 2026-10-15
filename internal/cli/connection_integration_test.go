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
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func TestConnectionFlagsValidateBeforeTicketRead(t *testing.T) {
	missing := "@" + filepath.Join(t.TempDir(), "missing-ticket")
	out := filepath.Join(t.TempDir(), "terminal")
	cases := [][]string{
		{"exec", missing}, {"connect", missing}, {"shell", missing, "--no-raw"},
		{"terminal", "open", missing, "--out", out}, {"jobs", "list", missing},
		{"files", "stat", missing, "/x"}, {"upload", missing, "local", "remote"},
		{"download", missing, "remote", "local"}, {"doctor", "--target", missing},
		{"check", missing}, {"capabilities", missing}, {"forward", missing, "--to", "localhost:80"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args[:1], ""), func(t *testing.T) {
			args = append(args, "--connect-timeout", "0s")
			err := Run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard, "test")
			if err == nil || !strings.Contains(err.Error(), "connection options") {
				t.Fatalf("expected connection validation, got %v", err)
			}
		})
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("terminal output created: %v", err)
	}
}

func TestConnectionOptionsRemoteDelimiterAndLifetime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command syntax")
	}
	ticket, _ := executionHost(t)
	var out bytes.Buffer
	args := []string{"exec", "--connect-timeout", "2s", "--keepalive-interval", "5ms", "--keepalive-timeout", "1s", ticket, "--", "sleep 0.1; printf '%s' --keepalive-interval"}
	if err := Run(context.Background(), args, strings.NewReader(""), &out, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "--keepalive-interval" {
		t.Fatalf("remote flags changed: %q", out.String())
	}
}

func TestTerminalConnectionConfigCompatibility(t *testing.T) {
	var old terminalConfig
	if err := json.Unmarshal([]byte(`{}`), &old); err != nil || old.Connection != nil {
		t.Fatalf("old config: %+v %v", old, err)
	}
	options := access.ConnectionOptions{ConnectTimeout: time.Second, KeepaliveInterval: 0, KeepaliveTimeout: 2 * time.Second}
	encoded, err := json.Marshal(terminalConfig{Connection: &options})
	if err != nil {
		t.Fatal(err)
	}
	var decoded terminalConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Connection == nil || *decoded.Connection != options {
		t.Fatalf("connection config lost: %s %v", encoded, err)
	}
}

func TestRestrictedCapabilitiesValidation(t *testing.T) {
	base := remoteCapabilities{Protocol: 1, OS: "linux", Arch: "amd64", Shell: "sh", SFTP: true}
	legacy := base
	if err := validateRemoteCapabilities(&legacy); err != nil || legacy.AccessMode != "full" {
		t.Fatalf("legacy: %+v %v", legacy, err)
	}
	for _, mode := range []string{"sftp", "sftp-readonly"} {
		valid := base
		valid.AccessMode = mode
		valid.SFTPReadOnly = mode == "sftp-readonly"
		if err := validateRemoteCapabilities(&valid); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*remoteCapabilities){
			func(c *remoteCapabilities) { c.Exec = true }, func(c *remoteCapabilities) { c.PTY = true },
			func(c *remoteCapabilities) { c.Env = true }, func(c *remoteCapabilities) { c.CWD = true },
			func(c *remoteCapabilities) { c.ExecSignal = true }, func(c *remoteCapabilities) { c.Forward = true },
			func(c *remoteCapabilities) { c.Jobs = true; c.JobsProtocol = 1 }, func(c *remoteCapabilities) { c.SFTP = false },
			func(c *remoteCapabilities) { c.SFTPReadOnly = !c.SFTPReadOnly },
		} {
			invalid := valid
			mutate(&invalid)
			if validateRemoteCapabilities(&invalid) == nil {
				t.Fatalf("accepted contradiction: %+v", invalid)
			}
		}
	}
	unknown := base
	unknown.AccessMode = "future"
	if validateRemoteCapabilities(&unknown) == nil {
		t.Fatal("accepted unknown mode")
	}
}

func TestSSHConfigOptionsBeforeTicketRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	err := sshConfig([]string{"@missing", "--out", dir, "--server-alive-interval", "1ms"}, io.Discard, io.Discard)
	if err == nil || strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected options error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("config created: %v", err)
	}
}

func TestRestrictedCheckAndDoctorRealSSH(t *testing.T) {
	for _, mode := range []string{"sftp", "sftp-readonly"} {
		t.Run(mode, func(t *testing.T) {
			a, h, err := access.NewPair(time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
			if err != nil {
				t.Fatal(err)
			}
			pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() { cancel(); ln.Close() })
			server := sshd.Server{Signer: signer, AuthorizedKey: pub, Shell: "unused-shell", WorkDir: t.TempDir(), AccessMode: sshd.AccessMode(mode)}
			go func() {
				for {
					c, e := ln.Accept()
					if e != nil {
						return
					}
					go server.ServeConn(ctx, c)
				}
			}()
			a.Addresses = []string{ln.Addr().String()}
			data, _ := json.Marshal(a)
			file := filepath.Join(t.TempDir(), "ticket.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := check(ctx, []string{"@" + file, "--json"}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			var result checkResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.OK || result.IdentityVerified || result.AccessMode != mode || result.User != "" {
				t.Fatalf("check: %s %v", out.String(), err)
			}
			stages, err := diagnoseTarget(ctx, "@"+file, "", access.DefaultRetryOptions())
			if err != nil {
				t.Fatal(err)
			}
			last := stages[len(stages)-1]
			if last.Stage != "identity" || last.OK || last.Code != "not_available_in_access_mode" {
				t.Fatalf("unexpected stages: %+v", stages)
			}
		})
	}
}

func TestSSHConfigCLIAliveOptions(t *testing.T) {
	ticket, _ := executionHost(t)
	dir := filepath.Join(t.TempDir(), "ssh")
	var out bytes.Buffer
	if err := sshConfig([]string{ticket, "--out", dir, "--server-alive-interval", "7s", "--server-alive-count-max", "5", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(out.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(metadata["config"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ServerAliveInterval 7") || !strings.Contains(string(data), "ServerAliveCountMax 5") || strings.Contains(string(data), "ControlMaster") {
		t.Fatalf("wrong config: %s", data)
	}
	if metadata["control_persist"] != "0s" {
		t.Fatalf("wrong metadata: %v", metadata)
	}
}
func testBlackholeCLI(t *testing.T, knownExit bool) {
	a, h, e := access.NewPair(time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if e != nil {
		t.Fatal(e)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	go func() {
		raw, e := ln.Accept()
		if e != nil {
			return
		}
		defer raw.Close()
		server, channels, requests, e := ssh.NewServerConn(raw, cfg)
		if e != nil {
			return
		}
		defer server.Close()
		go func() {
			for range requests {
			}
		}()
		for incoming := range channels {
			ch, reqs, e := incoming.Accept()
			if e != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range reqs {
					if req.Type == "exec" {
						req.Reply(true, nil)
						ch.Write([]byte("started\n"))
						if knownExit {
							ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{7}))
						}
					} else {
						req.Reply(false, nil)
					}
				}
			}()
		}
	}()
	a.Addresses = []string{ln.Addr().String()}
	data, _ := json.Marshal(a)
	p := filepath.Join(t.TempDir(), "ticket.json")
	os.WriteFile(p, data, 0600)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out, diag bytes.Buffer
	err := Run(ctx, []string{"exec", "@" + p, "--json", "--no-stdin", "--command", "hang", "--keepalive-interval", "10ms", "--keepalive-timeout", "60ms", "--retries", "0"}, nil, &out, &diag, "test")

	var result execResult
	if e := json.Unmarshal(out.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	var diagnostic struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if e := json.Unmarshal(diag.Bytes(), &diagnostic); e != nil {
		t.Fatalf("diagnostic: %s: %v", diag.String(), e)
	}
	if knownExit {
		var exit *ssh.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 7 || diagnostic.Error.Code != "remote_exit" || !result.ExitStatusKnown || result.ExitCode != 7 {
			t.Fatalf("known exit lost: result=%+v diagnostic=%s err=%v", result, diag.String(), err)
		}
	} else if !errors.Is(err, access.ErrKeepaliveTimeout) || diagnostic.Error.Code != "connection_lost" || result.ExitStatusKnown || result.TerminationReason != "connection_lost" || result.ExitCode != -1 || result.Stdout != "started\n" {
		t.Fatalf("timeout attribution lost: result=%+v diagnostic=%s err=%v", result, diag.String(), err)
	}
}
func TestConnectionBlackholeCLIJSON(t *testing.T) {
	t.Run("unknown", func(t *testing.T) { testBlackholeCLI(t, false) })
	t.Run("known_exit", func(t *testing.T) { testBlackholeCLI(t, true) })
}
