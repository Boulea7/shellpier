package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
)

func forwardListenerAddress(address string) (string, error) {
	if !strings.Contains(address, ":") {
		address = net.JoinHostPort("127.0.0.1", address)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("forward listen requires a loopback host and numeric port")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
		return "", errors.New("forward listener must bind a literal loopback address or localhost")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return "", errors.New("forward listen port must be numeric 0..65535")
	}
	return net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(int(port))), nil
}

func forward(parent context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("forward", stderr)
	listen := f.String("listen", "127.0.0.1:0", "loopback listener; a port alone uses 127.0.0.1")
	reverse := f.Bool("reverse", false, "listen on the remote host and connect to a local target")
	target := f.String("to", "", "destination host:port; on the control computer with --reverse")
	timeout := f.Duration("timeout", 0, "total forwarding lifetime; 0 uses ticket lifetime")
	machine := f.Bool("json", false, "emit listener metadata as JSON")
	iface := f.String("interface", "", "direct SSH network interface")
	retry := clientRetryFlags(f)
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 1 || *timeout < 0 {
		return errors.New("usage: shellpier forward TICKET --to HOST:PORT [--listen 127.0.0.1:0]")
	}
	localAddress, err := forwardListenerAddress(*listen)
	if err != nil {
		return err
	}
	destination, err := sshd.NormalizeForwardTarget(*target)
	if err != nil {
		return err
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	parent = retry.Context(parent)
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if *timeout > 0 {
		var done context.CancelFunc
		ctx, done = context.WithTimeout(ctx, *timeout)
		defer done()
	}
	if !ticket.ExpiresAt.IsZero() {
		var done context.CancelFunc
		ctx, done = context.WithDeadline(ctx, ticket.ExpiresAt)
		defer done()
	}
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	var listener net.Listener
	if *reverse {
		listener, err = client.Listen("tcp", localAddress)
	} else {
		listener, err = net.Listen("tcp", localAddress)
	}
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(); _ = listener.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	// A transport loss ends this listener, including all accepted connections.
	lost := make(chan error, 1)
	refused := make(chan error, 1)
	go func() { err := client.Wait(); lost <- err; cancel(); _ = listener.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = client.Close(); _ = listener.Close() })
	defer stop()
	event := struct {
		SchemaVersion int    `json:"schema_version"`
		Command       string `json:"command"`
		Event         string `json:"event"`
		Direction     string `json:"direction"`
		LocalAddress  string `json:"local_address,omitempty"`
		Target        string `json:"target"`
		RemoteAddress string `json:"remote_address,omitempty"`
		LocalTarget   string `json:"local_target,omitempty"`
	}{SchemaVersion: 1, Command: "forward", Event: "listening", Direction: "local", LocalAddress: listener.Addr().String(), Target: destination}
	if *reverse {
		event.Direction = "reverse"
		event.RemoteAddress = event.LocalAddress
		event.LocalAddress = ""
		event.LocalTarget = destination
	}
	if *machine {
		err = json.NewEncoder(stdout).Encode(event)
	} else {
		if *reverse {
			_, err = fmt.Fprintf(stdout, "Reverse forward listening on remote host: %s -> local %s\nKeep this process open; transport loss stops forwarding.\n", event.RemoteAddress, destination)
		} else {
			_, err = fmt.Fprintf(stdout, "Forward listening: %s -> %s\nKeep this process open; transport loss stops forwarding.\n", event.LocalAddress, destination)
		}
	}
	if err != nil {
		return err
	}
	slots := make(chan struct{}, sshd.MaxSessions)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			if ctx.Err() != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return ctx.Err()
				}
				select {
				case cause := <-refused:
					return fmt.Errorf("forward connection failed or target was not allowed; forwarding stopped: %w", cause)
				case cause := <-lost:
					return errors.Join(errors.New("SSH connection closed; forwarding stopped without replay"), cause)
				default:
					return ctx.Err()
				}
			}
			return access.ConnectionError(client, err)
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
			var remote net.Conn
			var err error
			if *reverse {
				remote, err = (&net.Dialer{}).DialContext(dialCtx, "tcp", destination)
			} else {
				remote, err = client.DialContext(dialCtx, "tcp", destination)
			}
			done()
			if err != nil {
				select {
				case refused <- err:
				default:
				}
				cancel()
				return
			}
			defer remote.Close()
			_ = sshd.BridgeForward(ctx, conn, remote)
		}()
	}
}
