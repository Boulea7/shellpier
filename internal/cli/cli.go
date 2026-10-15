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
  shellpier share [--relay URL] [--ttl 3h] [--keep-awake]
  shellpier host <host-ticket or @file> [--keep-awake]

Your AI Agent's computer:
  shellpier invite --relay URL --out NEW-DIRECTORY
  shellpier connect [--interface NAME] <access-ticket or @file> -- whoami
  shellpier exec TICKET --command 'COMMAND' [--timeout 30s] [--json]
  shellpier shell TICKET
  shellpier terminal open TICKET --out NEW-DIRECTORY
  shellpier jobs start TICKET --command 'COMMAND' --operation-id ID
  shellpier forward TICKET --listen 127.0.0.1:0 --to HOST:PORT
  shellpier forward TICKET --dynamic --listen 1080
  shellpier proxy TICKET --to HOST:PORT
  shellpier files list TICKET . [--json]
  shellpier ssh-config <access-ticket or @file> --out NEW-DIRECTORY
  shellpier upload <access-ticket or @file> LOCAL REMOTE
  shellpier download <access-ticket or @file> REMOTE LOCAL
  shellpier keygen --out NEW-DIRECTORY [--json]
  shellpier revoke <host-ticket or @file> [--timeout 10s]

Lifecycle and diagnostics:
  shellpier doctor [--relay URL] [--json]
  shellpier targets add NAME --ticket FILE
  shellpier service plan NAME --ticket HOST-FILE
  shellpier mcp serve
  shellpier inspect <ticket or @file> [--json]
  shellpier check <access-ticket or @file> [--timeout 20s] [--json]
  shellpier capabilities TICKET [--json]
  shellpier sessions [--json]
  shellpier status --state FILE
  shellpier stop --state FILE
  shellpier cleanup --dir GENERATED-SSH-CONFIG-DIRECTORY
  shellpier relay --init-key NEW-FILE
  shellpier relay --token-file FILE [--listen 127.0.0.1:8080]
  shellpier completion bash|zsh|fish|powershell
  shellpier version [--json]
  shellpier schema [COMMAND]

Sharing defaults: foreground, three-hour lifetime, current user's permissions.
Direct connections need reachable peers. Across NAT, configure a relay.
Use --ttl 0 for a persistent session; the relay must allow it.
Tickets are credentials. Share them privately.
Run shellpier <command> --help for options.
`

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) (result error) {
	asJSON := JSONRequested(args)
	diagnostics := stderr
	args = normalizeGlobalFlags(args)
	defer func() {
		if errors.Is(result, flag.ErrHelp) {
			result = nil
		}
		var reported *ReportedError
		if asJSON && result != nil && !errors.As(result, &reported) {
			command := "unknown"
			if len(args) > 0 {
				command = args[0]
			}
			result = WriteJSONError(diagnostics, command, result)
		}
	}()
	if asJSON {
		if len(args) > 0 && localHelpRequested(args) {
			return schema([]string{args[0], "--json"}, stdout, diagnostics)
		}
		stderr = io.Discard
	}
	if len(args) == 0 {
		_, err := io.WriteString(stdout, help)
		return err
	}
	switch args[0] {
	case "help":
		if asJSON {
			return schema(args[1:], stdout, diagnostics)
		}
		if len(args) > 1 && args[1] != "--help" && args[1] != "-h" {
			return Run(ctx, append(args[1:], "--help"), stdin, stdout, stderr, version)
		}
		_, err := io.WriteString(stdout, help)
		return err
	case "--help", "-h":
		_, err := io.WriteString(stdout, help)
		return err
	case "version", "--version":
		return printVersion(args[1:], stdout, stderr, version)
	case "share":
		return share(ctx, args[1:], stdout, stderr)
	case "host":
		return host(ctx, args[1:], stdout, stderr)
	case "invite":
		return invite(ctx, args[1:], stdout, stderr)
	case "connect", "exec", "proxy":
		return connect(ctx, args[0], args[1:], stdin, stdout, stderr)
	case "shell":
		return shell(ctx, args[1:], stdin, stdout, stderr)
	case "files":
		return files(ctx, args[1:], stdin, stdout, stderr)
	case "terminal":
		return terminal(ctx, args[1:], stdin, stdout, stderr)
	case "jobs":
		return jobsCommand(ctx, args[1:], stdout, stderr)
	case "forward":
		return forward(ctx, args[1:], stdout, stderr)
	case "targets":
		return targets(ctx, args[1:], stdin, stdout, stderr)
	case "service":
		return service(ctx, args[1:], stdin, stdout, stderr)
	case "mcp":
		return mcp(ctx, args[1:], stdin, stdout, stderr, version)
	case "sessions":
		return sessions(ctx, args[1:], stdout, stderr)
	case "schema":
		return schema(args[1:], stdout, stderr)
	case "capabilities":
		return capabilities(ctx, args[1:], stdout, stderr)
	case "ssh-config":
		return sshConfig(args[1:], stdout, stderr)
	case "doctor":
		return doctor(ctx, args[1:], stdout, stderr)
	case "inspect":
		return inspect(args[1:], stdout, stderr)
	case "check":
		return check(ctx, args[1:], stdout, stderr)
	case "upload", "download":
		return transferWithInputAndProgress(ctx, args[0], args[1:], stdin, stdout, stderr, diagnostics)
	case "keygen":
		return keygen(args[1:], stdout, stderr)
	case "completion":
		return completion(args[1:], stdout, stderr)
	case "status", "stop":
		return control(ctx, args[0], args[1:], stdout, stderr)
	case "revoke":
		return revoke(ctx, args[1:], stdout, stderr)
	case "cleanup":
		return cleanup(args[1:], stdout, stderr)
	case "relay":
		return runRelay(ctx, args[1:], stdout, stderr)
	default:
		return &UsageError{Err: fmt.Errorf("unknown command; run shellpier help")}
	}
}
