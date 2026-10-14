package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Boulea7/shellpier/internal/access"
)

func connect(ctx context.Context, mode string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f := flags(mode, stderr)
	var retry *access.RetryOptions
	if mode != "proxy" {
		retry = clientRetryFlags(f)
	}
	iface := f.String("interface", "", "constrain direct TCP routing to this interface (macOS/Linux)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() < 1 {
		return errors.New("provide an access ticket or @file")
	}
	t, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err := t.Validate("access"); err != nil {
		return err
	}
	if mode == "proxy" {
		if f.NArg() != 1 {
			return errors.New("proxy accepts exactly one ticket")
		}
		return access.Proxy(ctx, t, *iface, stdin, stdout)
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	commands := f.Args()[1:]
	if len(commands) > 0 && commands[0] == "--" {
		commands = commands[1:]
	}
	command := strings.Join(commands, " ")
	if command == "" {
		command = "whoami"
	}
	client, err := access.DialRetry(ctx, t, *iface, *retry)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	return session.Run(command)
}

// ssh-config permits the natural "ticket --out directory" spelling.
func sshConfig(args []string, stdout, stderr io.Writer) error {
	ticketArg, args := ticketArgument(args)
	f := flags("ssh-config", stderr)
	dir := f.String("out", "", "new private output directory")
	if err := f.Parse(args); err != nil {
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
	t, err := access.Read(ticketArg)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := access.WriteConfig(t, *dir, exe)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "SSH config: %s\nUse: ssh -F %s shellpier whoami\n", path, displayArg(path))
	return nil
}
