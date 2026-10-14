// Package sshd provides a temporary, public-key-only SSH command and SFTP server.
package sshd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const MaxCommandBytes = 64 * 1024
const MaxSessions = 8
const DefaultHandshakeTimeout = 10 * time.Second

type Server struct {
	Signer           ssh.Signer
	AuthorizedKey    ssh.PublicKey
	Shell            string
	WorkDir          string
	HandshakeTimeout time.Duration
}

func (s *Server) ServeConn(ctx context.Context, conn net.Conn) error {
	if s.Signer == nil || s.AuthorizedKey == nil {
		return fmt.Errorf("host signer and authorized key are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	timeout := s.HandshakeTimeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	deadline := time.Now().Add(timeout)
	if expires, ok := ctx.Deadline(); ok && expires.Before(deadline) {
		deadline = expires
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SSH handshake deadline: %w", err)
	}
	config := &ssh.ServerConfig{MaxAuthTries: 3, PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(key.Marshal(), s.AuthorizedKey.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("public key rejected")
	}}
	config.AddHostKey(s.Signer)
	sc, chans, reqs, e := ssh.NewServerConn(conn, config)
	if e != nil {
		return e
	}
	defer sc.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear SSH handshake deadline: %w", err)
	}
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxSessions)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only command and SFTP sessions are supported")
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			nc.Reject(ssh.ResourceShortage, "session limit reached")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			<-sem
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer func() { <-sem }(); s.session(ctx, ch, requests) }()
	}
	cancel()
	wg.Wait()
	return sc.Wait()
}
func (s *Server) session(ctx context.Context, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			io.WriteString(ch.Stderr(), "PTY is unsupported; use non-interactive SSH exec commands.\n")
			req.Reply(false, nil)
		case "exec":
			var payload struct{ Command string }
			e := ssh.Unmarshal(req.Payload, &payload)
			if e != nil || len(payload.Command) == 0 || len(payload.Command) > MaxCommandBytes {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			execCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() { ssh.DiscardRequests(reqs); cancel() }()
			s.execute(execCtx, ch, payload.Command)
			return
		case "subsystem":
			var payload struct{ Name string }
			if ssh.Unmarshal(req.Payload, &payload) != nil || payload.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			srv, e := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.WorkDir))
			if e != nil {
				req.Reply(false, nil)
				return
			}
			req.Reply(true, nil)
			go ssh.DiscardRequests(reqs)
			e = srv.Serve()
			srv.Close()
			status := uint32(0)
			if e != nil && e != io.EOF {
				status = 1
			}
			sendStatus(ch, status)
			return
		default:
			req.Reply(false, nil)
		}
	}
}
func sendStatus(ch ssh.Channel, status uint32) {
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}
func (s *Server) execute(ctx context.Context, ch ssh.Channel, command string) {
	cmd, e := platform.Command(s.Shell, command, s.WorkDir)
	if e != nil {
		io.WriteString(ch.Stderr(), e.Error()+"\n")
		sendStatus(ch, 127)
		return
	}
	// An explicit stdin pipe avoids exec.Wait waiting forever on a copy from an open SSH channel.
	stdin, e := cmd.StdinPipe()
	if e != nil {
		sendStatus(ch, 127)
		return
	}
	cmd.WaitDelay = time.Second
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	stop, e := platform.Start(cmd)
	if e != nil {
		stdin.Close()
		io.WriteString(ch.Stderr(), e.Error()+"\n")
		sendStatus(ch, 127)
		return
	}
	defer stop()
	go func() { io.Copy(stdin, ch); stdin.Close() }()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	e = cmd.Wait()
	close(done)
	stdin.Close()
	status := uint32(0)
	if e != nil {
		status = 1
		var exit *exec.ExitError
		if errors.As(e, &exit) {
			code := exit.ExitCode()
			if code >= 0 {
				status = uint32(code)
			} else {
				status = 255
			}
		}
	}
	sendStatus(ch, status)
}
