package cli

func featureCommandSpecs() []commandSpec {
	jobSpec := command("jobs", "Manage bounded host-owned background tasks", "jobs ACTION TICKET [JOB-ID] [flags]", "metadata", "job state and bounded output; enabled in full mode", []string{"network", "optional_remote_exec"}, nil)
	for _, action := range []string{"start", "status", "operation", "list", "logs", "wait", "cancel"} {
		timeout := "20s"
		if action == "start" || action == "wait" {
			timeout = "3h0m0s"
		}
		flags := specFlags(jsonSpecFlag, interfaceSpecFlag, retrySpecFlags, []flagSpec{specFlag("timeout", "duration", timeout, "Job runtime for start; total client timeout otherwise")})
		effects := []string{"network", "remote_read"}
		usage := "jobs " + action + " TICKET"
		switch action {
		case "start":
			flags = append(flags, specFlag("command", "string", "", "Exact remote shell command"), specFlag("script", "string", "", "Local UTF-8 script"), specFlag("cwd", "string", "", "Remote working directory"), specFlag("env", "string[]", "", "Repeatable remote NAME=VALUE"), specFlag("operation-id", "string", "", "Stable start ID; generated if omitted; retained until host exit"))
			effects = []string{"network", "remote_exec", "remote_write"}
		case "logs":
			flags = append(flags, specFlag("stdout-cursor", "int64", "0", "Absolute stdout offset"), specFlag("stderr-cursor", "int64", "0", "Absolute stderr offset"), specFlag("limit", "int", "32768", "Bytes per stream; at most 32768"))
		case "cancel":
			effects = []string{"network", "remote_write"}
		}
		if action == "operation" {
			usage += " OPERATION-ID"
		} else if action != "start" && action != "list" {
			usage += " JOB-ID"
		}
		jobSpec.Subcommands = append(jobSpec.Subcommands, command(action, "Host-managed job "+action, usage+" [flags]", "metadata_or_bytes", "job response; wait returns nonzero when the job fails; logs JSON uses base64", effects, flags))
	}
	forwardSpec := command("forward", "Forward loopback ports through SSH", "forward TICKET (--to HOST:PORT | --dynamic | --local LISTEN=HOST:PORT...) [flags]", "foreground", "listening address and direction; repeated local emits one listening event with a forwards array after every bind succeeds; destination failures preserve listeners; cancellation, expiry or SSH loss stops the group", []string{"listener", "network"}, specFlags(jsonSpecFlag, interfaceSpecFlag, retrySpecFlags, []flagSpec{specFlag("listen", "string", "127.0.0.1:0", "Loopback bind address or port; remote with reverse"), specFlag("reverse", "bool", "false", "Listen on the remote computer and connect to a local destination"), specFlag("dynamic", "bool", "false", "Local SOCKS5 CONNECT with remote DNS; excludes to and reverse"), specFlag("local", "string[]", "", "Repeat LISTEN=HOST:PORT, at most 16 local forwards sharing one SSH connection; excludes explicit listen/to/reverse/dynamic"), specFlag("to", "string", "", "Destination HOST:PORT on the other side; required unless dynamic or local"), specFlag("timeout", "duration", "0s", "Forward lifetime; zero uses ticket lifetime")}))
	targetSpec := command("targets", "Manage private local ticket references", "targets ACTION [NAME] [flags]", "metadata", "safe alias metadata; credentials are neither copied nor renewed", []string{"local_read", "optional_local_write"}, nil)
	for _, action := range []string{"add", "list", "show", "remove"} {
		flags := specFlags(jsonSpecFlag, []flagSpec{specFlag("config-dir", "string", "", "Private config directory; SHELLPIER_CONFIG_DIR or user config/shellpier")})
		effects := []string{"local_read"}
		usage := "targets " + action
		if action != "list" {
			usage += " NAME"
		}
		if action == "add" {
			flags = append(flags, specFlag("ticket", "string", "", "Existing private ticket file to reference"))
			effects = append(effects, "local_write")
		} else if action == "remove" {
			effects = append(effects, "local_delete")
		}
		targetSpec.Subcommands = append(targetSpec.Subcommands, command(action, "Local target reference "+action, usage+" [flags]", "metadata", "safe target metadata; remove preserves the ticket", effects, flags))
	}
	serviceSpec := command("service", "Manage an explicitly installed current-user host service", "service ACTION NAME [flags]", "metadata", "reviewable plan or service-manager result; finite tickets still expire", []string{"local_read", "optional_local_write", "optional_service_control"}, nil)
	for _, action := range []string{"plan", "install", "start", "status", "stop", "uninstall"} {
		flags := specFlags(jsonSpecFlag, []flagSpec{specFlag("config-dir", "string", "", "Private configuration directory"), specFlag("dry-run", "bool", "false", "Describe without modifying files or manager state")})
		effects := []string{"local_read"}
		if action == "plan" || action == "install" {
			flags = append(flags, specFlag("access-mode", "string", "full", "full, sftp, or sftp-readonly; file modes disable commands and forwarding"), specFlag("root", "string", "", "Share only this directory through SFTP; remote paths start at /"))
			flags = append(flags, specFlag("ticket", "string", "", "Existing host ticket file or target:NAME"), specFlag("autostart", "bool", "false", "Opt into startup at user login; install does not start now"), specFlag("shell", "string", "auto", "Target shell"), specFlag("work-dir", "string", "", "Remote starting directory"), specFlag("keep-awake", "bool", "false", "Inhibit sleep while host runs"), specFlag("allow-jobs", "bool", "true", "Allow jobs in full mode; false disables"), specFlag("allow-forward", "string[]", "", "Optional forwarding target limit; full mode otherwise allows all"))
		}
		if action != "plan" && action != "status" {
			effects = append(effects, "local_write", "service_control")
		}
		serviceSpec.Subcommands = append(serviceSpec.Subcommands, command(action, "Current-user service "+action, "service "+action+" NAME [flags]", "metadata", "service plan or status; installation is explicit and does not start the host", effects, flags))
	}
	mcpSpec := command("mcp", "Expose bounded CLI operations over MCP stdio", "mcp serve [flags]", "jsonrpc", "newline-delimited JSON-RPC; stdout contains protocol only", []string{"stdio", "optional_network", "optional_local_write", "optional_remote_write"}, nil)
	mcpSpec.Subcommands = []commandSpec{command("serve", "Run the MCP stdio server", "mcp serve [flags]", "jsonrpc", "MCP 2026-07-28 with 2025-11-25 initialization compatibility", mcpSpec.Effects, []flagSpec{specFlag("timeout", "duration", "1m0s", "Maximum per-tool time; at most 5m"), specFlag("max-concurrent", "int", "4", "Concurrent calls; 1..16"), specFlag("max-output", "int", "262144", "Captured tool bytes; 1024..1048576")})}
	return []commandSpec{jobSpec, forwardSpec, targetSpec, serviceSpec, mcpSpec}
}
