package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Boulea7/shellpier/internal/access"
)

func targets(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier targets add NAME --ticket FILE [--config-dir DIR] [--json]\n       shellpier targets list|show NAME|remove NAME [--config-dir DIR] [--json]\nAliases reference private ticket files; use target:NAME in connection commands. SHELLPIER_CONFIG_DIR overrides the default user config directory. Tickets are not copied or renewed.")
		return err
	}
	action := args[0]
	switch action {
	case "add", "list", "show", "remove":
	default:
		return errors.New("unknown targets action")
	}
	f := flags("targets "+action, stderr)
	dir := f.String("config-dir", "", "private config directory; defaults to SHELLPIER_CONFIG_DIR or user config/shellpier")
	machine := f.Bool("json", false, "emit safe target metadata")
	var ticket string
	if action == "add" {
		f.StringVar(&ticket, "ticket", "", "private ticket file to reference")
	}
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	n := 1
	if action == "list" {
		n = 0
	}
	if f.NArg() != n {
		return errors.New("targets requires one name, except list")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var result any
	switch action {
	case "add":
		value, err := access.AddTarget(*dir, f.Arg(0), ticket)
		if err != nil {
			return err
		}
		result = value
	case "show":
		value, err := access.ShowTarget(*dir, f.Arg(0))
		if err != nil {
			return err
		}
		result = value
	case "list":
		value, err := access.ListTargets(*dir)
		if err != nil {
			return err
		}
		result = value
	case "remove":
		if err := access.RemoveTarget(*dir, f.Arg(0)); err != nil {
			return err
		}
		result = map[string]any{"name": f.Arg(0), "removed": true, "ticket_deleted": false}
	}
	if *machine {
		return json.NewEncoder(stdout).Encode(result)
	}
	if values, ok := result.([]access.TargetInfo); ok {
		for _, item := range values {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.Name, item.Status, item.TicketFile); err != nil {
				return err
			}
		}
		return nil
	}
	if value, ok := result.(access.TargetInfo); ok {
		_, err := fmt.Fprintf(stdout, "%s\t%s\t%s\n", value.Name, value.Status, value.TicketFile)
		return err
	}
	_, err := fmt.Fprintln(stdout, "Target reference removed; ticket file preserved.")
	return err
}
