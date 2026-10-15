package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func shell(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (result error) {
	f := flags("shell", stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 0, "total terminal timeout; 0 uses the ticket lifetime")
	terminal := f.String("term", "xterm-256color", "remote terminal type")
	columns := f.Int("cols", 0, "terminal columns; 0 follows the local terminal")
	rows := f.Int("rows", 0, "terminal rows; 0 follows the local terminal")
	noRaw := f.Bool("no-raw", false, "forward input without putting the local terminal in raw mode")
	command := f.String("command", "", "run this exact command in the terminal instead of a shell")
	cwd := f.String("cwd", "", "remote working directory")
	var env environmentValues
	f.Var(&env, "env", "remote environment NAME=VALUE; repeatable")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 1 || *timeout < 0 || *columns < 0 || *rows < 0 || *columns > 1000 || *rows > 1000 {
		return errors.New("usage: shellpier shell TICKET [--cols 80 --rows 24]; dimensions must be 0..1000 and timeout nonnegative")
	}
	if len(*terminal) == 0 || len(*terminal) > 64 || strings.IndexFunc(*terminal, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
		return errors.New("term must contain 1..64 printable ASCII characters without spaces")
	}
	if len(*command) > 64*1024 || strings.ContainsRune(*command, 0) {
		return errors.New("command must be at most 65536 bytes without NUL")
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	ctx = retry.Context(ctx)
	fd, hasFD := streamFD(stdin)
	isTerminal := hasFD && term.IsTerminal(fd)
	if !*noRaw && !isTerminal {
		return errors.New("shell needs a local terminal; use --no-raw for explicit piped PTY input, or exec for commands")
	}
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err := ticket.Validate("access"); err != nil {
		return err
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	if err := configureSession(session, *cwd, env); err != nil {
		return err
	}
	size := func() (int, int) {
		width, height := 80, 24
		if isTerminal {
			if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
				width, height = min(w, 1000), min(h, 1000)
			}
		}
		if *columns != 0 {
			width = *columns
		}
		if *rows != 0 {
			height = *rows
		}
		return width, height
	}
	width, height := size()
	if err := session.RequestPty(*terminal, height, width, ssh.TerminalModes{}); err != nil {
		return fmt.Errorf("target rejected PTY; use exec on targets without terminal support: %w", err)
	}
	if !*noRaw {
		original, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, term.Restore(fd, original)) }()
	}
	restoreOutput, err := terminalOutputMode(stdout)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, restoreOutput()) }()
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	if *command == "" {
		err = session.Shell()
	} else {
		err = session.Start(*command)
	}
	if err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	if isTerminal && (*columns == 0 || *rows == 0) {
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					w, h := size()
					if w != width || h != height {
						if err := session.WindowChange(h, w); err != nil {
							return
						}
						width, height = w, h
					}
				}
			}
		}()
	}
	err = access.ConnectionError(client, session.Wait())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func streamFD(stream any) (int, bool) {
	file, ok := stream.(interface{ Fd() uintptr })
	if !ok {
		return 0, false
	}
	return int(file.Fd()), true
}
