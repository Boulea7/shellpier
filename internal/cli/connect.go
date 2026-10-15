package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
)

func connect(ctx context.Context, mode string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f := flags(mode, stderr)
	var retry *clientConnectionOptions
	if mode != "proxy" {
		retry = clientRetryFlags(f)
	}
	iface := f.String("interface", "", "constrain direct TCP routing to this interface (macOS/Linux)")
	var timeout time.Duration
	var commandText, script, cwd string
	var asJSON, noStdin bool
	var maxOutput int
	var env environmentValues
	if mode != "proxy" {
		f.DurationVar(&timeout, "timeout", 0, "total command timeout; 0 uses the ticket lifetime")
		f.StringVar(&commandText, "command", "", "exact remote shell command")
		f.StringVar(&script, "script", "", "read a remote shell command from a local UTF-8 file")
		f.StringVar(&cwd, "cwd", "", "remote working directory for this command")
		f.Var(&env, "env", "remote environment NAME=VALUE; repeatable")
		f.BoolVar(&asJSON, "json", false, "capture bounded stdout, stderr, and exit code as JSON")
		f.BoolVar(&noStdin, "no-stdin", false, "send EOF instead of forwarding standard input")
		f.IntVar(&maxOutput, "max-output", 1024*1024, "maximum captured bytes per stream with --json (up to 16 MiB)")
	}
	local, commands, separated := splitCommandArgs(f, args)
	if err := parseFlags(f, local); err != nil {
		return err
	}
	if f.NArg() < 1 {
		return errors.New("provide an access ticket or @file")
	}
	if mode == "proxy" {
		if f.NArg() != 1 || separated {
			return errors.New("proxy accepts exactly one ticket")
		}
		t, err := access.Read(f.Arg(0))
		if err != nil {
			return err
		}
		if err := t.Validate("access"); err != nil {
			return err
		}
		return access.Proxy(ctx, t, *iface, stdin, stdout)
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	ctx = retry.Context(ctx)
	if separated && f.NArg() != 1 {
		return errors.New("provide one ticket before -- and the remote command after it")
	}
	if !separated {
		commands = f.Args()[1:]
	}
	if timeout < 0 || maxOutput < 1 || maxOutput > 16*1024*1024 {
		return errors.New("timeout must be nonnegative and max-output must be 1..16777216")
	}
	command, err := remoteCommand(mode, commandText, script, commands)
	if err != nil {
		return err
	}
	t, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err := t.Validate("access"); err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	client, err := access.DialRetry(ctx, t, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	if err := configureSession(session, cwd, env); err != nil {
		return err
	}
	if noStdin {
		stdin = nil
	}
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	if !asJSON {
		err := access.ConnectionError(client, session.Run(command))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	out, diagnostic := &limitedCapture{limit: maxOutput}, &limitedCapture{limit: maxOutput}
	session.Stdout, session.Stderr = out, diagnostic
	err = access.ConnectionError(client, session.Run(command))
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	result := executionResult(mode, err, out, diagnostic)
	if writeErr := json.NewEncoder(stdout).Encode(result); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

// ssh-config permits the natural "ticket --out directory" spelling.
func sshConfig(args []string, stdout, stderr io.Writer) error {
	ticketArg, args := ticketArgument(args)
	f := flags("ssh-config", stderr)
	dir := f.String("out", "", "new private output directory")
	asJSON := f.Bool("json", false, "emit generated SSH configuration paths")
	options := access.DefaultSSHConfigOptions()
	f.DurationVar(&options.ServerAliveInterval, "server-alive-interval", options.ServerAliveInterval, "OpenSSH alive probe interval in whole seconds; 0 disables")
	f.IntVar(&options.ServerAliveCountMax, "server-alive-count-max", options.ServerAliveCountMax, "maximum unanswered OpenSSH alive probes")
	f.DurationVar(&options.ControlPersist, "control-persist", options.ControlPersist, "optional Unix private connection reuse lifetime; 0 disables")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if ticketArg == "" && f.NArg() == 1 {
		ticketArg = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("ssh-config accepts one ticket and --out directory")
	}
	if ticketArg == "" || *dir == "" {
		return errors.New("usage: shellpier ssh-config TICKET --out NEW-DIRECTORY")
	}
	if err := options.Validate(*dir); err != nil {
		return err
	}
	t, err := access.Read(ticketArg)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := access.WriteConfigWithOptions(t, *dir, exe, options)
	if err != nil {
		return err
	}
	if *asJSON {
		metadata := map[string]any{"config": path, "host": "shellpier", "control_persist": options.ControlPersist.String()}
		if options.ControlPersist > 0 {
			metadata["control_path"] = filepath.Join(filepath.Dir(path), "control.sock")
		}
		return json.NewEncoder(stdout).Encode(metadata)
	}
	fmt.Fprintf(stdout, "SSH config: %s\nUse: ssh -F %s shellpier whoami\n", path, displayArg(path))
	return nil
}
