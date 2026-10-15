package cli

import (
	"context"
	"flag"

	"github.com/Boulea7/shellpier/internal/access"
)

type clientConnectionOptions struct {
	access.RetryOptions
	Connection access.ConnectionOptions
}

func (o *clientConnectionOptions) Validate() error {
	if err := o.RetryOptions.Validate(); err != nil {
		return err
	}
	return o.Connection.Validate()
}
func (o *clientConnectionOptions) Context(ctx context.Context) context.Context {
	return access.WithConnectionOptions(ctx, o.Connection)
}

func clientRetryFlags(f *flag.FlagSet) *clientConnectionOptions {
	options := &clientConnectionOptions{RetryOptions: access.DefaultRetryOptions(), Connection: access.DefaultConnectionOptions()}
	f.IntVar(&options.Retries, "retries", options.Retries, "connection retries before remote work; 0 disables")
	f.DurationVar(&options.MinDelay, "retry-min", options.MinDelay, "initial connection retry delay")
	f.DurationVar(&options.MaxDelay, "retry-max", options.MaxDelay, "maximum connection retry delay")
	f.DurationVar(&options.Connection.ConnectTimeout, "connect-timeout", options.Connection.ConnectTimeout, "maximum connection setup duration per candidate")
	f.DurationVar(&options.Connection.KeepaliveInterval, "keepalive-interval", options.Connection.KeepaliveInterval, "SSH probe interval; 0 disables probes")
	f.DurationVar(&options.Connection.KeepaliveTimeout, "keepalive-timeout", options.Connection.KeepaliveTimeout, "maximum wait for an SSH probe reply")
	return options
}
