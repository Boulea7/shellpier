package cli

import (
	"flag"

	"github.com/Boulea7/shellpier/internal/access"
)

func clientRetryFlags(f *flag.FlagSet) *access.RetryOptions {
	options := access.DefaultRetryOptions()
	f.IntVar(&options.Retries, "retries", options.Retries, "connection retries before remote work; 0 disables")
	f.DurationVar(&options.MinDelay, "retry-min", options.MinDelay, "initial connection retry delay")
	f.DurationVar(&options.MaxDelay, "retry-max", options.MaxDelay, "maximum connection retry delay")
	return &options
}
