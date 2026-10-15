package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
)

type flagSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Description string `json:"description"`
}
type commandSpec struct {
	Name        string        `json:"name"`
	Summary     string        `json:"summary"`
	Usage       string        `json:"usage"`
	Effects     []string      `json:"effects"`
	StdioMode   string        `json:"stdio_mode"`
	Output      string        `json:"output"`
	Flags       []flagSpec    `json:"flags"`
	Subcommands []commandSpec `json:"subcommands,omitempty"`
}

func specFlag(name, kind, value, description string) flagSpec {
	return flagSpec{name, kind, value, description}
}
func specFlags(groups ...[]flagSpec) []flagSpec {
	result := []flagSpec{}
	for _, g := range groups {
		result = append(result, g...)
	}
	return result
}

var jsonSpecFlag = []flagSpec{specFlag("json", "bool", "false", "Emit machine-readable output; global JSON errors use stderr")}
var interfaceSpecFlag = []flagSpec{specFlag("interface", "string", "", "Direct network interface; platform support varies")}
var retrySpecFlags = []flagSpec{
	specFlag("connect-timeout", "duration", "10s", "Maximum connection setup time per attempt"),
	specFlag("keepalive-interval", "duration", "15s", "SSH probe interval; zero disables end-to-end probes"),
	specFlag("keepalive-timeout", "duration", "45s", "Maximum wait for one SSH keepalive reply"),
	specFlag("retries", "int", "2", "Connection retries before remote work; never replay operations"),
	specFlag("retry-min", "duration", "200ms", "Initial connection retry delay"),
	specFlag("retry-max", "duration", "3s", "Maximum connection retry delay"),
}
var hostSpecFlags = []flagSpec{
	specFlag("access-mode", "string", "full", "full, sftp, or sftp-readonly; file modes disable commands and forwarding"),
	specFlag("root", "string", "", "Share only this directory through SFTP; remote paths start at /"),
	specFlag("keep-awake", "bool", "false", "Temporarily inhibit automatic sleep"),
	specFlag("shell", "string", "auto", "Target shell: auto, powershell, cmd, sh"),
	specFlag("work-dir", "string", "", "Remote starting directory"),
	specFlag("reconnect-min", "duration", "1s", "Minimum host relay reconnect delay"),
	specFlag("reconnect-max", "duration", "30s", "Maximum host relay reconnect delay"),
	specFlag("allow-jobs", "bool", "true", "Allow jobs in full mode; false disables"),
	specFlag("max-jobs", "int", "128", "Host-lifetime capacity including completed jobs; 1..1024"),
	specFlag("max-running-jobs", "int", "4", "Concurrent jobs; 1..64 and no more than max-jobs"),
	specFlag("job-log-limit", "int", "1048576", "Retained log bytes per job stream; 1024..16777216"),
	specFlag("allow-forward", "string[]", "", "Optional forwarding target limit; full mode otherwise allows all"),
}
var execSpecFlags = specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{
	specFlag("timeout", "duration", "0s", "Total command timeout; zero uses ticket lifetime"),
	specFlag("command", "string", "", "Exact remote shell command"),
	specFlag("script", "string", "", "Local UTF-8 command file"),
	specFlag("cwd", "string", "", "Remote command working directory"),
	specFlag("env", "string[]", "", "Repeatable remote NAME=VALUE"),
	specFlag("no-stdin", "bool", "false", "Send EOF instead of forwarding stdin"),
	specFlag("max-output", "int", "1048576", "Captured bytes per stream; maximum 16777216"),
})

func command(name, summary, usage, stdio, output string, effects []string, flags []flagSpec) commandSpec {
	return commandSpec{Name: name, Summary: summary, Usage: "shellpier " + usage, Effects: effects, StdioMode: stdio, Output: output, Flags: specFlags(flags)}
}

// commandSpecs is static discovery data, never reconstructed from human help output.
var commandSpecs = buildCommandSpecs()

func buildCommandSpecs() []commandSpec {
	local := []string{"local_read"}
	remote := []string{"network", "remote_exec"}
	write := []string{"local_write"}
	commands := []commandSpec{
		command("share", "Start temporary current-user SSH access", "share [flags]", "foreground", "session metadata; ticket is secret unless --output is used", []string{"listener", "local_write", "optional_relay"}, specFlags(hostSpecFlags, jsonSpecFlag, interfaceSpecFlag, []flagSpec{
			specFlag("relay", "string", "", "Relay URL; SHELLPIER_RELAY_URL may supply default"), specFlag("token-file", "string", "", "Private relay enrollment key file"), specFlag("ttl", "duration", "3h0m0s", "Lifetime; zero explicitly requests persistence"), specFlag("listen", "string", "0.0.0.0:0", "Direct SSH bind address"), specFlag("output", "string", "", "New private access ticket file")})),
		command("host", "Run a controller-created target invitation", "host TICKET [flags]", "foreground", "session metadata", []string{"network", "local_write"}, specFlags(hostSpecFlags, jsonSpecFlag)),
		command("invite", "Create a relay invitation and isolated client files", "invite --relay URL --out DIR [flags]", "metadata", "generated file paths", []string{"network", "local_write", "relay_create"}, []flagSpec{specFlag("relay", "string", "", "Relay URL; SHELLPIER_RELAY_URL may supply default"), specFlag("token-file", "string", "", "Private relay enrollment key file"), specFlag("ttl", "duration", "3h0m0s", "Lifetime; zero explicitly requests persistence"), specFlag("out", "string", "", "New private output directory")}),
		command("connect", "Run a remote command; default whoami", "connect [flags] TICKET -- COMMAND", "stream", "remote streams or bounded JSON capture", remote, execSpecFlags),
		command("exec", "Run an explicit remote command", "exec [flags] TICKET -- COMMAND", "stream", "remote streams or bounded JSON capture", remote, execSpecFlags),
		command("proxy", "Transport SSH bytes or bridge a remote TCP destination", "proxy [flags] TICKET [--to HOST:PORT]", "binary", "raw bytes on stdin/stdout; --to authenticates SSH and connects to the destination; JSON is unsupported", []string{"network"}, specFlags(interfaceSpecFlag, retrySpecFlags, []flagSpec{specFlag("to", "string", "", "Destination HOST:PORT through the authenticated host; omit for OpenSSH transport"), specFlag("timeout", "duration", "0s", "Total proxy lifetime with --to; zero uses ticket lifetime; retry flags also require --to"), specFlag("json", "bool", "false", "Unsupported for binary streams; true returns an error")})),
		command("ssh-config", "Write an isolated pinned SSH configuration", "ssh-config TICKET --out DIR [flags]", "metadata", "generated SSH config path and optional private control socket path", write, []flagSpec{specFlag("out", "string", "", "New private output directory"), specFlag("server-alive-interval", "duration", "15s", "OpenSSH encrypted keepalive interval; whole seconds, zero disables"), specFlag("server-alive-count-max", "int", "3", "OpenSSH maximum unanswered keepalive probes"), specFlag("control-persist", "duration", "0s", "Unix OpenSSH idle reuse time; zero disables, 1s..1h in whole seconds")}),
		command("doctor", "Inspect local configuration and optional target connectivity", "doctor [flags]", "metadata", "local diagnostics and target stages; relay health is not SSH proof", []string{"local_read", "optional_network", "optional_remote_exec"}, specFlags(jsonSpecFlag, interfaceSpecFlag, retrySpecFlags, []flagSpec{specFlag("relay", "string", "", "Relay health URL; environment may supply default"), specFlag("target", "string", "", "Ticket reference for SSH, identity and capability checks"), specFlag("timeout", "duration", "20s", "Total target diagnostic timeout")})),
		command("status", "Inspect one local running session", "status --state FILE", "metadata", "local control status JSON", []string{"local_read", "loopback_control"}, []flagSpec{specFlag("state", "string", "", "Private state.json path")}),
		command("stop", "Stop one local session", "stop --state FILE", "metadata", "stop confirmation", []string{"local_read", "loopback_control", "revoke"}, []flagSpec{specFlag("state", "string", "", "Private state.json path")}),
		command("revoke", "Revoke a relay invitation", "revoke TICKET [flags]", "metadata", "revocation confirmation", []string{"network", "revoke"}, []flagSpec{specFlag("timeout", "duration", "10s", "Total revocation timeout")}),
		command("cleanup", "Remove owned generated client files", "cleanup --dir DIR", "metadata", "cleanup confirmation; unrelated files retained", []string{"local_read", "local_delete"}, []flagSpec{specFlag("dir", "string", "", "Generated config directory; does not revoke access")}),
		command("relay", "Run an authenticated WebSocket relay", "relay [flags]", "foreground", "listener metadata", []string{"listener", "optional_local_write"}, []flagSpec{specFlag("listen", "string", "127.0.0.1:8080", "Listener bind address"), specFlag("token-file", "string", "", "Enrollment key file"), specFlag("init-key", "string", "", "Create a private key and exit"), specFlag("max-sessions", "int", "64", "Session capacity"), specFlag("max-ttl", "duration", "24h0m0s", "Maximum nonpersistent lifetime"), specFlag("allow-persistent", "bool", "false", "Explicitly permit TTL zero"), specFlag("tls-cert", "string", "", "Native HTTPS PEM certificate"), specFlag("tls-key", "string", "", "Native HTTPS PEM key")}),
		command("inspect", "Inspect a ticket without connecting", "inspect TICKET [flags]", "metadata", "credential-free ticket summary", local, jsonSpecFlag),
		command("check", "Verify remote host and account", "check TICKET [flags]", "metadata", "identity and pinned key result", remote, specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{specFlag("timeout", "duration", "20s", "Total identity check timeout")})),
		command("upload", "Upload files, a directory or stdin without overwriting", "upload [flags] TICKET LOCAL|- REMOTE", "stream_or_metadata", "files-from selects relative files under source/destination roots; LOCAL=- reads payload stdin once; summary on stdout and optional progress on stderr; payload streams exclude recursive/resume/preserve-times/dry-run/skip-existing", []string{"optional_local_read", "optional_local_write", "network", "remote_write"}, transferSpecFlags()),
		command("download", "Download files or a directory, or stream to stdout", "download [flags] TICKET REMOTE LOCAL|-", "metadata_or_bytes", "files-from selects relative files under source/destination roots; LOCAL=- writes raw payload bytes without a summary and rejects JSON; optional progress uses stderr; payload streams exclude recursive/resume/preserve-times/dry-run/skip-existing", []string{"network", "optional_local_read", "optional_local_write"}, transferSpecFlags()),
		command("keygen", "Generate a private key directory", "keygen --out DIR [flags]", "metadata", "key paths and fingerprint", write, specFlags(jsonSpecFlag, []flagSpec{specFlag("out", "string", "", "New private output directory")})),
		command("completion", "Print a static shell completion script", "completion bash|zsh|fish|powershell", "script", "completion script; never installs it", []string{}, []flagSpec{}),
		command("version", "Show build metadata", "version [--json]", "metadata", "version text or build metadata", []string{}, jsonSpecFlag),
		command("help", "Show command help", "help", "metadata", "human-readable help", []string{}, []flagSpec{}),
		command("sessions", "Discover private local session state", "sessions [flags]", "metadata", "local session summaries; no ticket secrets", []string{"local_read", "loopback_control"}, specFlags(jsonSpecFlag, []flagSpec{specFlag("dir", "string", "system_temp_directory", "Parent directory to inspect; no remote discovery")})),
		command("schema", "Describe command interfaces offline", "schema [COMMAND] [--json]", "metadata", "schema_version 1 command specifications", []string{}, jsonSpecFlag),
	}
	fileBase := specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{specFlag("timeout", "duration", "5m0s", "Total file operation timeout")})
	files := command("files", "Operate on remote paths with SFTP", "files ACTION [flags] TICKET PATH [DEST]", "metadata_or_bytes", "metadata or raw bytes; JSON read uses base64", []string{"network", "optional_remote_write"}, []flagSpec{})
	for _, action := range []string{"list", "stat", "read", "mkdir", "rename", "remove"} {
		ff := specFlags(fileBase)
		effect := []string{"network", "remote_read"}
		if action == "list" {
			ff = append(ff, specFlag("limit", "int64", "1000", "Maximum returned entries; up to 10000"))
		}
		if action == "read" {
			ff = append(ff, specFlag("limit", "int64", "1048576", "Maximum bytes; up to 16777216"), specFlag("offset", "int64", "0", "Starting byte offset"))
		}
		if action == "mkdir" || action == "rename" || action == "remove" {
			ff = append(ff, specFlag("dry-run", "bool", "false", "Describe without writing"))
			effect = []string{"network", "remote_write"}
		}
		if action == "mkdir" {
			ff = append(ff, specFlag("parents", "bool", "false", "Create missing parent directories"))
		}
		usage := "files " + action + " [flags] TICKET PATH"
		if action == "rename" {
			usage += " DEST"
		}
		mode, output := "metadata", "path metadata or mutation result"
		if action == "read" {
			mode = "bytes_or_json"
			output = "raw bytes or JSON base64 data"
		}
		files.Subcommands = append(files.Subcommands, command(action, "Remote path "+action, usage, mode, output, effect, ff))
	}
	commands = append(commands, files)
	terminalFlags := specFlags(interfaceSpecFlag, retrySpecFlags, []flagSpec{specFlag("term", "string", "xterm-256color", "Terminal type"), specFlag("cols", "int", "0", "Terminal columns; zero follows local terminal"), specFlag("rows", "int", "0", "Terminal rows; zero follows local terminal"), specFlag("no-raw", "bool", "false", "Do not change local stdin terminal mode"), specFlag("command", "string", "", "Remote terminal command"), specFlag("cwd", "string", "", "Remote working directory"), specFlag("env", "string[]", "", "Repeatable remote NAME=VALUE"), specFlag("timeout", "duration", "0s", "Total terminal timeout; zero uses ticket lifetime")})
	commands = append(commands, command("shell", "Open a remote PTY when the target supports it", "shell [flags] TICKET", "terminal", "interactive terminal bytes", remote, terminalFlags))
	terminalSpec := command("terminal", "Manage a persistent remote PTY", "terminal ACTION [flags]", "metadata_or_bytes", "metadata and bounded base64 output snapshots", []string{"network", "local_write", "remote_exec"}, []flagSpec{})
	terminalOpenFlags := specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{
		specFlag("out", "string", "", "New private terminal directory"), specFlag("command", "string", "", "Exact remote command; empty starts shell"), specFlag("cwd", "string", "", "Remote working directory"), specFlag("cols", "int", "80", "Terminal columns"), specFlag("rows", "int", "24", "Terminal rows"), specFlag("term", "string", "xterm-256color", "Terminal type"), specFlag("timeout", "duration", "3h0m0s", "Lifetime bounded by ticket expiry"), specFlag("env", "string[]", "", "Repeatable remote NAME=VALUE"),
	})
	terminalOpenFlags = append(terminalOpenFlags, specFlag("log-limit", "int64", "0", "Private output.log byte limit; 0 disables, maximum 67108864"))
	terminalControlFlags := specFlags(jsonSpecFlag, []flagSpec{specFlag("state", "string", "", "Private terminal state file"), specFlag("cursor", "uint64", "0", "Absolute output byte offset"), specFlag("limit", "int", "65536", "Maximum read bytes"), specFlag("wait", "duration", "0s", "Read wait; at most 30s"), specFlag("text", "string", "", "Exact input bytes"), specFlag("file", "string", "", "Input file; - reads stdin"), specFlag("cols", "int", "80", "Resize columns"), specFlag("rows", "int", "24", "Resize rows"), specFlag("signal", "string", "", "SSH signal name")})
	terminalControlFlags = append(terminalControlFlags, specFlag("operation-id", "string", "", "Stable write ID; retained for worker lifetime, capacity 256"))
	terminalSpec.Subcommands = append(terminalSpec.Subcommands, command("open", "Start a managed PTY", "terminal open TICKET --out DIR [flags]", "metadata", "private state path", []string{"network", "local_write", "remote_exec"}, terminalOpenFlags))
	for _, action := range []string{"read", "write", "resize", "signal", "status", "wait", "operation", "close"} {
		allowed := map[string]bool{"json": true, "state": true}
		switch action {
		case "read":
			allowed["cursor"] = true
			allowed["limit"] = true
			allowed["wait"] = true
		case "write":
			allowed["text"] = true
			allowed["file"] = true
			allowed["operation-id"] = true
		case "wait":
			allowed["wait"] = true
		case "operation":
			allowed["operation-id"] = true
		case "resize":
			allowed["cols"] = true
			allowed["rows"] = true
		case "signal":
			allowed["signal"] = true
		}
		actual := []flagSpec{}
		for _, flag := range terminalControlFlags {
			if allowed[flag.Name] {
				if action == "wait" && flag.Name == "wait" {
					flag.Default, flag.Description = "30s", "Wait for closure; 0..30s, timeout returns closed=false"
				}
				actual = append(actual, flag)
			}
		}
		terminalSpec.Subcommands = append(terminalSpec.Subcommands, command(action, "Managed PTY "+action, "terminal "+action+" --state FILE [flags]", "metadata_or_bytes", "terminal operation result", []string{"loopback_control"}, actual))
	}
	terminalSpec.Subcommands = append(terminalSpec.Subcommands, command("tail", "Read raw managed terminal output", "terminal tail --state FILE [flags]", "bytes", "raw bytes on stdout, dropped-byte diagnostics on stderr; reads up to 1 MiB once or follows until closure; cancellation stops only the reader; success does not describe the remote exit code", []string{"local_read", "loopback_control"}, []flagSpec{
		specFlag("state", "string", "", "Private terminal state file"),
		specFlag("cursor", "uint64", "0", "Absolute output byte offset"),
		specFlag("follow", "bool", "false", "Continue reading until terminal output closes"),
		specFlag("timeout", "duration", "30m0s", "Total reader lifetime; greater than zero and at most 24h"),
		specFlag("json", "bool", "false", "Unsupported for raw terminal output; true returns an error"),
	}))
	commands = append(commands, terminalSpec)
	commands = append(commands, command("capabilities", "Query actual remote platform capabilities", "capabilities TICKET [flags]", "metadata", "target capability response; not inferred from controller", []string{"network"}, specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{specFlag("timeout", "duration", "20s", "Total capability query timeout")})))
	for i := range commands {
		switch commands[i].Name {
		case "invite", "ssh-config", "status", "stop", "revoke", "cleanup", "relay":
			commands[i].Flags = specFlags(commands[i].Flags, jsonSpecFlag)
		}
	}
	return append(commands, featureCommandSpecs()...)
}
func transferSpecFlags() []flagSpec {
	return specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{
		specFlag("timeout", "duration", "5m0s", "Maximum transfer duration"),
		specFlag("recursive", "bool", "false", "Transfer a tree; mutually exclusive with resume and files-from"),
		specFlag("exclude", "string[]", "", "Relative slash glob or basename exclusion; no **"),
		specFlag("files-from", "string", "", "Local relative-file list under source root; - reads stdin; excludes recursive/exclude/resume/payload streams"),
		specFlag("from0", "bool", "false", "Split files-from records on NUL instead of LF/CRLF; requires files-from"),
		specFlag("max-files", "int", "10000", "Recursive or selected file limit; 1..100000"),
		specFlag("max-depth", "int", "32", "Recursive or selected path nesting limit; 0..128"),
		specFlag("resume", "bool", "false", "Resume one file using a private journal and owned partial"),
		specFlag("resume-state", "string", "", "Private resume journal path; requires resume"),
		specFlag("progress", "string", "", "jsonl emits progress events on stderr"),
		specFlag("dry-run", "bool", "false", "Read-only transfer plan; incompatible with resume"),
		specFlag("preserve-times", "bool", "false", "Preserve new file and directory mtime in whole seconds"),
		specFlag("skip-existing", "bool", "false", "Skip existing files without comparing their contents; incompatible with resume"),
		specFlag("bwlimit", "int64", "0", "Shared transfer bytes per second; zero is unlimited"),
	})
}
func findCommandSpec(name string) (commandSpec, bool) {
	for _, s := range commandSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return commandSpec{}, false
}
func commandNames() string {
	names := make([]string, 0, len(commandSpecs))
	for _, s := range commandSpecs {
		names = append(names, s.Name)
	}
	return strings.Join(names, " ")
}
func schema(args []string, stdout, stderr io.Writer) error {
	input, args := ticketArgument(args)
	f := flags("schema", stderr)
	asJSON := f.Bool("json", false, "emit command specifications as JSON")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if input == "" && f.NArg() == 1 {
		input = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("schema accepts one optional command")
	}
	specs := commandSpecs
	if input != "" {
		s, ok := findCommandSpec(input)
		if !ok {
			return errors.New("unknown schema command")
		}
		specs = []commandSpec{s}
	}
	capabilities := map[string]any{"scope": "local_controller", "os": runtime.GOOS, "arch": runtime.GOARCH, "direct_interface_binding": runtime.GOOS == "linux" || runtime.GOOS == "darwin", "remote_capabilities": "query capabilities with a target ticket; never inferred locally"}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(struct {
			SchemaVersion int            `json:"schema_version"`
			Commands      []commandSpec  `json:"commands"`
			Capabilities  map[string]any `json:"capabilities"`
		}{1, specs, capabilities})
	}
	for _, s := range specs {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\n  %s\n", s.Name, s.Summary, s.Usage); err != nil {
			return err
		}
	}
	return nil
}
