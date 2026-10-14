package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// parseFlags permits local options around positionals, while preserving everything after --.
func parseFlags(f *flag.FlagSet, args []string) error {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := f.Lookup(name)
		if option == nil || hasValue {
			continue
		}
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 == len(args) {
			return &UsageError{Err: fmt.Errorf("flag needs an argument: -%s", name)}
		}
		i++
		options = append(options, args[i])
	}
	err := f.Parse(append(append(options, "--"), positional...))
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		return &UsageError{Err: err}
	}
	return err
}

func normalizeGlobalFlags(args []string) []string {
	var prefix []string
	for len(args) > 0 && (args[0] == "--json" || strings.HasPrefix(args[0], "--json=")) {
		prefix = append(prefix, args[0])
		args = args[1:]
	}
	if len(prefix) == 0 {
		return args
	}
	if len(args) == 0 {
		return append([]string{"schema"}, prefix...)
	}
	position := 1
	if (args[0] == "files" || args[0] == "terminal") && len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		position = 2
	}
	result := append([]string{}, args[:position]...)
	result = append(result, prefix...)
	return append(result, args[position:]...)
}

func localHelpRequested(args []string) bool {
	if len(args) < 2 {
		return false
	}
	selected, _ := findCommandSpec(args[0])
	for index := 1; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			for _, sub := range selected.Subcommands {
				if sub.Name == arg {
					selected = sub
					break
				}
			}
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if hasValue {
			continue
		}
		for _, option := range selected.Flags {
			if option.Name == name && option.Type != "bool" {
				index++
				break
			}
		}
	}
	return false
}

func splitCommandArgs(f *flag.FlagSet, args []string) (local, remote []string, separated bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := f.Lookup(name)
		if option == nil || hasValue {
			continue
		}
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		i++
	}
	return args, nil, false
}
