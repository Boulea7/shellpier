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

	"github.com/Boulea7/shellpier/internal/jobs"
	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/crypto/ssh"
	"os"
)

const MaxCommandBytes = 64 * 1024
const MaxSessions = 8
const DefaultHandshakeTimeout = 10 * time.Second

type Server struct {
	AccessMode       AccessMode
	FileRoot         *os.Root
	Signer           ssh.Signer
	AuthorizedKey    ssh.PublicKey
	Shell            string
	WorkDir          string
	HandshakeTimeout time.Duration
	Jobs             *jobs.Manager
	Forward          *ForwardPolicy
	Reverse          *ReverseForwardLimits
}

func (s *Server) ServeConn(ctx context.Context, conn net.Conn) error {
	if _, err := ParseAccessMode(string(s.AccessMode)); err != nil {
		conn.Close()
		return err
	}
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
	var wg sync.WaitGroup
	sem := make(chan struct{}, MaxSessions)
	var reverse *reverseForwarder
	if s.fullAccess() {
		reverse = newReverseForwarder(ctx, sc, sem, s.Reverse)
		defer reverse.Close()
	}
	go s.globalRequests(reqs, reverse)
	for nc := range chans {
		if nc.ChannelType() != "session" && nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "unsupported SSH channel")
			continue
		}
		if nc.ChannelType() == "direct-tcpip" && !s.fullAccess() {
			nc.Reject(ssh.Prohibited, "forwarding disabled by access mode")
			continue
		}
		select {
		case sem <- struct{}{}:
		default:
			nc.Reject(ssh.ResourceShortage, "session limit reached")
			continue
		}
		if nc.ChannelType() == "direct-tcpip" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				_ = s.Forward.Serve(ctx, nc)
			}()
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
func sendStatus(ch ssh.Channel, status uint32) {
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}
func (s *Server) execute(ctx context.Context, ch ssh.Channel, command string, env []string) {
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
	cmd.Env = mergeEnvironment(os.Environ(), env)
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
