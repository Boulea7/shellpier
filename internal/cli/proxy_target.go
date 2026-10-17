package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"sync"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
)

func proxyTarget(parent context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (resultErr error) {
	f := flags("proxy", stderr)
	target := f.String("to", "", "destination host:port reached through the authenticated host")
	iface := f.String("interface", "", "constrain direct TCP routing to this interface (macOS/Linux)")
	timeout := f.Duration("timeout", 0, "total proxy lifetime; 0 uses ticket lifetime")
	machine := f.Bool("json", false, "unsupported for binary proxy streams")
	retry := clientRetryFlags(f)
	local, _, separated := splitCommandArgs(f, args)
	if err := parseFlags(f, local); err != nil {
		return err
	}
	if f.NArg() != 1 || separated {
		return errors.New("proxy accepts exactly one ticket")
	}
	if *machine {
		return &UsageError{Err: errors.New("proxy carries binary bytes and does not support --json")}
	}
	hasTarget := false
	targetOnlyFlag := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == "to" {
			hasTarget = true
		}
		if option.Name != "interface" && option.Name != "json" {
			targetOnlyFlag = true
		}
	})
	if !hasTarget && targetOnlyFlag {
		return errors.New("proxy connection options require --to HOST:PORT")
	}
	var destination string
	var err error
	if hasTarget {
		destination, err = sshd.NormalizeForwardTarget(*target)
		if err != nil {
			return err
		}
		if *timeout < 0 {
			return errors.New("proxy timeout must be nonnegative")
		}
		if err := retry.Validate(); err != nil {
			return err
		}
	}
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err := ticket.Validate("access"); err != nil {
		return err
	}
	if !hasTarget {
		return access.Proxy(parent, ticket, *iface, stdin, stdout)
	}
	ctx, cancel := context.WithCancel(retry.Context(parent))
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
	defer func() {
		if err := proxyContextError(ctx); err != nil {
			resultErr = err
		}
	}()
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	dialCtx, done := context.WithTimeout(ctx, sshd.DefaultForwardDialTimeout)
	remote, err := client.DialContext(dialCtx, "tcp", destination)
	done()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return access.ConnectionError(client, err)
	}
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			_ = remote.Close()
			if input, ok := stdin.(io.Closer); ok {
				_ = input.Close()
			}
			if output, ok := stdout.(io.Closer); ok {
				_ = output.Close()
			}
		})
	}
	defer remote.Close()
	stop := context.AfterFunc(ctx, cleanup)
	defer stop()
	lost := make(chan error, 1)
	go func() { lost <- client.Wait() }()
	upload := make(chan error, 1)
	download := make(chan error, 1)
	go func() {
		var err error
		if stdin != nil {
			_, err = io.Copy(remote, stdin)
		}
		if err == nil {
			if half, ok := remote.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			}
		}
		upload <- err
	}()
	go func() { _, err := io.Copy(stdout, remote); download <- err }()
	ctxDone := ctx.Done()
	var result error
	// Either direction may half-close first while the other still carries bytes.
	for upload != nil || download != nil {
		select {
		case err := <-upload:
			upload = nil
			if err != nil {
				result = errors.Join(result, err)
				cleanup()
			}
		case err := <-download:
			download = nil
			if err != nil {
				result = errors.Join(result, err)
				cleanup()
			}
		case err := <-lost:
			lost = nil
			result = errors.Join(result, errors.New("SSH connection closed; proxy stopped without replay"), err)
			cleanup()
		case <-ctxDone:
			ctxDone = nil
			cleanup()
			result = ctx.Err()
		}
		// Reader and Writer alone cannot interrupt a blocked local operation.
		if result != nil {
			if _, ok := stdin.(io.Closer); upload != nil && !ok {
				return access.ConnectionError(client, result)
			}
			if _, ok := stdout.(io.Closer); download != nil && !ok {
				return access.ConnectionError(client, result)
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if result != nil {
		return access.ConnectionError(client, result)
	}
	// The SSH mux closes its channels before publishing its Wait error. A reply
	// confirms that normal stream EOF did not hide an already lost transport.
	confirmed := make(chan error, 1)
	go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); confirmed <- err }()
	timer := time.NewTimer(retry.Connection.ConnectTimeout)
	defer timer.Stop()
	select {
	case err := <-confirmed:
		return access.ConnectionError(client, err)
	case err := <-lost:
		return access.ConnectionError(client, errors.Join(errors.New("SSH connection closed; proxy stopped without replay"), err))
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.Join(errors.New("SSH proxy completion confirmation timed out"), context.DeadlineExceeded)
	}
}

func proxyContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// The SSH client's independent expiry timer can close the transport before
	// this context publishes its error. The deadline still bounds every return.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
