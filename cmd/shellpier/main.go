package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Boulea7/shellpier/internal/cli"
	"golang.org/x/crypto/ssh"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version); err != nil {
		fmt.Fprintln(os.Stderr, "shellpier:", err)
		var remote *ssh.ExitError
		if errors.As(err, &remote) && remote.ExitStatus() > 0 && remote.ExitStatus() < 256 {
			os.Exit(remote.ExitStatus())
		}
		os.Exit(1)
	}
}
