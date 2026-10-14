// Package cli implements the portable ShellPier command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

const help = `ShellPier — SSH access to your own machines.

Target computer:
  shellpier share [--relay URL] [--ttl 1h] [--keep-awake]
  shellpier host <host-ticket or @file> [--keep-awake]

Your AI Agent's computer:
  shellpier invite --relay URL --out NEW-DIRECTORY
  shellpier connect [--interface NAME] <access-ticket or @file> -- whoami
  shellpier ssh-config <access-ticket or @file> --out NEW-DIRECTORY
  shellpier revoke <host-ticket or @file>

Lifecycle and diagnostics:
  shellpier doctor [--relay URL] [--json]
  shellpier status --state FILE
  shellpier stop --state FILE
  shellpier cleanup --dir GENERATED-SSH-CONFIG-DIRECTORY
  shellpier relay --init-key NEW-FILE
  shellpier relay --token-file FILE [--listen 127.0.0.1:8080]

Default: foreground, one-hour lifetime, current user's permissions.
Direct mode needs peer reachability; campus/hotspot NAT usually needs a relay.
Relay creation needs SHELLPIER_RELAY_KEY or --token-file. No public relay is bundled.
Use --ttl 0 for an explicitly persistent session (relay operator must also allow it).
Native binary: modern Windows, Linux, macOS. Windows 7/8: use legacy/shellpier.cmd.
Tickets contain credentials. Share them privately, and never commit them.
SSH exec and SFTP are supported; graphical desktop control and PTY are not.
`

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) (result error) {
	defer func() {
		if errors.Is(result, flag.ErrHelp) {
			result = nil
		}
	}()
	if len(args) == 0 {
		_, err := io.WriteString(stdout, help)
		return err
	}
	switch args[0] {
	case "help", "--help", "-h":
		_, err := io.WriteString(stdout, help)
		return err
	case "version", "--version":
		_, err := fmt.Fprintln(stdout, "shellpier", version)
		return err
	case "share":
		return share(ctx, args[1:], stdout, stderr)
	case "host":
		return host(ctx, args[1:], stdout, stderr)
	case "invite":
		return invite(ctx, args[1:], stdout, stderr)
	case "connect", "proxy":
		return connect(ctx, args[0], args[1:], stdin, stdout, stderr)
	case "ssh-config":
		return sshConfig(args[1:], stdout, stderr)
	case "doctor":
		return doctor(ctx, args[1:], stdout, stderr)
	case "status", "stop":
		return control(ctx, args[0], args[1:], stdout, stderr)
	case "revoke":
		return revoke(ctx, args[1:], stdout)
	case "cleanup":
		return cleanup(args[1:], stdout, stderr)
	case "relay":
		return runRelay(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q; run shellpier help", args[0])
	}
}
