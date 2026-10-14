package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Boulea7/shellpier/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version); err != nil {
		var reported *cli.ReportedError
		if !errors.As(err, &reported) {
			fmt.Fprintln(os.Stderr, "shellpier:", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
