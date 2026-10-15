package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

type localForward struct {
	Listen string `json:"listen"`
	Target string `json:"target"`
}
type localForwardFlags []localForward

func (v *localForwardFlags) String() string { return "" }
func (v *localForwardFlags) Set(value string) error {
	if len(*v) >= 16 {
		return errors.New("at most 16 local forward listeners are supported")
	}
	listen, target, ok := strings.Cut(value, "=")
	if !ok {
		return errors.New("local forward requires LISTEN=HOST:PORT")
	}
	listen, err := forwardListenerAddress(listen)
	if err != nil {
		return err
	}
	target, err = sshd.NormalizeForwardTarget(target)
	if err != nil {
		return err
	}
	*v = append(*v, localForward{listen, target})
	return nil
}

// forwardMultiple binds every listener before readiness and shares one transport
// and admission budget. Destination failures affect only the accepted stream.
func forwardMultiple(ctx context.Context, client *ssh.Client, mappings []localForward, machine bool, stdout, stderr io.Writer) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listeners := make([]net.Listener, 0, len(mappings))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	actual := append([]localForward(nil), mappings...)
	for i, mapping := range mappings {
		listener, err := net.Listen("tcp", mapping.Listen)
		if err != nil {
			return err
		}
		listeners = append(listeners, listener)
		actual[i].Listen = listener.Addr().String()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if machine {
		if err := json.NewEncoder(stdout).Encode(struct {
			SchemaVersion int            `json:"schema_version"`
			Command       string         `json:"command"`
			Event         string         `json:"event"`
			Direction     string         `json:"direction"`
			Forwards      []localForward `json:"forwards"`
		}{1, "forward", "listening", "local", actual}); err != nil {
			return err
		}
	} else {
		for _, mapping := range actual {
			if _, err := fmt.Fprintf(stdout, "Forward listening: %s -> %s\n", mapping.Listen, mapping.Target); err != nil {
				return err
			}
		}
	}
	stop := context.AfterFunc(ctx, func() {
		_ = client.Close()
		for _, listener := range listeners {
			_ = listener.Close()
		}
	})
	defer stop()
	lost := make(chan error, 1)
	go func() { err := client.Wait(); lost <- err; cancel() }()
	slots := make(chan struct{}, sshd.MaxSessions)
	var accepts, workers sync.WaitGroup
	var warning sync.Once
	failed := make(chan error, 1)
	for i, listener := range listeners {
		accepts.Add(1)
		go func(listener net.Listener, target string) {
			defer accepts.Done()
			for {
				conn, err := listener.Accept()
				if err != nil {
					if ctx.Err() == nil {
						select {
						case failed <- err:
						default:
						}
						cancel()
					}
					return
				}
				select {
				case slots <- struct{}{}:
				default:
					_ = conn.Close()
					continue
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer func() { <-slots }()
					defer conn.Close()
					dialCtx, done := context.WithTimeout(ctx, sshd.DefaultForwardDialTimeout)
					remote, err := pendingForwardDial(dialCtx, client, conn, "tcp", target)
					done()
					if err != nil {
						warning.Do(func() {
							_, _ = io.WriteString(stderr, "Forward destination connection failed; listener remains available.\n")
						})
						return
					}
					defer remote.Close()
					_ = sshd.BridgeForward(ctx, conn, remote)
				}()
			}
		}(listener, actual[i].Target)
	}
	<-ctx.Done()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	_ = client.Close()
	accepts.Wait()
	workers.Wait()
	if err := parent.Err(); err != nil {
		return err
	}
	select {
	case err := <-failed:
		return err
	default:
	}
	select {
	case err := <-lost:
		return errors.Join(errors.New("SSH connection closed; forwarding stopped without replay"), err)
	default:
		return ctx.Err()
	}
}

// pendingForwardDial retains the caller's admission slot until SSH resolves the
// channel-open. DialContext alone leaves an unbounded hidden Dial goroutine on
// timeout. Closing the local stream bounds its wait without freeing admission.
func pendingForwardDial(ctx context.Context, client *ssh.Client, local net.Conn, network, address string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	completed := make(chan result, 1)
	go func() { conn, err := client.Dial(network, address); completed <- result{conn, err} }()
	select {
	case got := <-completed:
		if err := ctx.Err(); err != nil {
			if got.conn != nil {
				_ = got.conn.Close()
			}
			return nil, err
		}
		return got.conn, got.err
	case <-ctx.Done():
		_ = local.Close()
		// Parent cancellation closes the transport, releasing pending SSH requests.
		got := <-completed
		if got.conn != nil {
			_ = got.conn.Close()
		}
		return nil, ctx.Err()
	}
}
