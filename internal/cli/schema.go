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
	specFlag("retries", "int", "2", "Connection retries before remote work; never replay operations"),
	specFlag("retry-min", "duration", "200ms", "Initial connection retry delay"),
	specFlag("retry-max", "duration", "3s", "Maximum connection retry delay"),
}
var hostSpecFlags = []flagSpec{
	specFlag("keep-awake", "bool", "false", "Temporarily inhibit automatic sleep"),
	specFlag("shell", "string", "auto", "Target shell: auto, powershell, cmd, sh"),
	specFlag("work-dir", "string", "", "Remote starting directory"),
	specFlag("reconnect-min", "duration", "1s", "Minimum host relay reconnect delay"),
	specFlag("reconnect-max", "duration", "30s", "Maximum host relay reconnect delay"),
	specFlag("allow-jobs", "bool", "false", "Allow bounded host-managed jobs across SSH disconnects"),
	specFlag("max-jobs", "int", "128", "Host-lifetime capacity including completed jobs; 1..1024"),
	specFlag("max-running-jobs", "int", "4", "Concurrent jobs; 1..64 and no more than max-jobs"),
	specFlag("job-log-limit", "int", "1048576", "Retained log bytes per job stream; 1024..16777216"),
	specFlag("allow-forward", "string[]", "", "Exact allowed HOST:PORT; repeat at most 32 times"),
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
		command("proxy", "Transport bytes for OpenSSH ProxyCommand", "proxy [flags] TICKET", "binary", "SSH transport bytes", []string{"network"}, interfaceSpecFlag),
		command("ssh-config", "Write an isolated pinned SSH configuration", "ssh-config TICKET --out DIR", "metadata", "generated SSH config path", write, []flagSpec{specFlag("out", "string", "", "New private output directory")}),
		command("doctor", "Inspect local configuration and optional target connectivity", "doctor [flags]", "metadata", "local diagnostics and target stages; relay health is not SSH proof", []string{"local_read", "optional_network", "optional_remote_exec"}, specFlags(jsonSpecFlag, interfaceSpecFlag, retrySpecFlags, []flagSpec{specFlag("relay", "string", "", "Relay health URL; environment may supply default"), specFlag("target", "string", "", "Ticket reference for SSH, identity and capability checks"), specFlag("timeout", "duration", "20s", "Total target diagnostic timeout")})),
		command("status", "Inspect one local running session", "status --state FILE", "metadata", "local control status JSON", []string{"local_read", "loopback_control"}, []flagSpec{specFlag("state", "string", "", "Private state.json path")}),
		command("stop", "Stop one local session", "stop --state FILE", "metadata", "stop confirmation", []string{"local_read", "loopback_control", "revoke"}, []flagSpec{specFlag("state", "string", "", "Private state.json path")}),
		command("revoke", "Revoke a relay invitation", "revoke TICKET [flags]", "metadata", "revocation confirmation", []string{"network", "revoke"}, []flagSpec{specFlag("timeout", "duration", "10s", "Total revocation timeout")}),
		command("cleanup", "Remove owned generated client files", "cleanup --dir DIR", "metadata", "cleanup confirmation; unrelated files retained", []string{"local_read", "local_delete"}, []flagSpec{specFlag("dir", "string", "", "Generated config directory; does not revoke access")}),
		command("relay", "Run an authenticated WebSocket relay", "relay [flags]", "foreground", "listener metadata", []string{"listener", "optional_local_write"}, []flagSpec{specFlag("listen", "string", "127.0.0.1:8080", "Listener bind address"), specFlag("token-file", "string", "", "Enrollment key file"), specFlag("init-key", "string", "", "Create a private key and exit"), specFlag("max-sessions", "int", "64", "Session capacity"), specFlag("max-ttl", "duration", "24h0m0s", "Maximum nonpersistent lifetime"), specFlag("allow-persistent", "bool", "false", "Explicitly permit TTL zero"), specFlag("tls-cert", "string", "", "Native HTTPS PEM certificate"), specFlag("tls-key", "string", "", "Native HTTPS PEM key")}),
		command("inspect", "Inspect a ticket without connecting", "inspect TICKET [flags]", "metadata", "credential-free ticket summary", local, jsonSpecFlag),
		command("check", "Verify remote host and account", "check TICKET [flags]", "metadata", "identity and pinned key result", remote, specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{specFlag("timeout", "duration", "20s", "Total identity check timeout")})),
		command("upload", "Upload a file or directory without overwriting", "upload [flags] TICKET LOCAL REMOTE", "metadata", "byte/file/directory counts, single-file SHA-256 and warnings; optional stderr progress", []string{"local_read", "optional_local_write", "network", "remote_write"}, transferSpecFlags()),
		command("download", "Download a file or directory without overwriting", "download [flags] TICKET REMOTE LOCAL", "metadata", "byte/file/directory counts, single-file SHA-256 and warnings; optional stderr progress", []string{"network", "local_write"}, transferSpecFlags()),
		command("keygen", "Generate a private key directory", "keygen --out DIR [flags]", "metadata", "key paths and fingerprint", write, specFlags(jsonSpecFlag, []flagSpec{specFlag("out", "string", "", "New private output directory")})),
		command("completion", "Print a static shell completion script", "completion bash|zsh|fish|powershell", "script", "completion script; never installs it", []string{}, []flagSpec{}),
		command("version", "Show build metadata", "version [--json]", "metadata", "version text or build metadata", []string{}, jsonSpecFlag),
		command("help", "Show command help", "help", "metadata", "human-readable help", []string{}, []flagSpec{}),
		command("sessions", "Discover private local session state", "sessions [flags]", "metadata", "local session summaries; no ticket secrets", []string{"local_read", "loopback_control"}, specFlags(jsonSpecFlag, []flagSpec{specFlag("dir", "string", "system_temp_directory", "Parent directory to inspect; no remote discovery")})),
		command("schema", "Describe command interfaces offline", "schema [COMMAND] [--json]", "metadata", "schema_version 1 command specifications", []string{}, jsonSpecFlag),
	}
	fileBase := specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{specFlag("timeout", "duration", "5m0s", "Total file operation timeout")})
	files := command("files", "Operate on remote paths with SFTP", "files ACTION [flags] TICKET PATH [DEST]", "metadata_or_bytes", "metadata or raw bytes; JSON read uses base64", []string{"network", "optional_remote_write"}, []flagSpec{})
	for _, action := range []string{"list", "stat", "read", "checksum", "mkdir", "rename", "remove"} {
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
	commands = append(commands, terminalSpec)
	commands = append(commands, command("capabilities", "Query actual remote platform capabilities", "capabilities TICKET [flags]", "metadata", "target capability response; not inferred from controller", []string{"network"}, specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{specFlag("timeout", "duration", "20s", "Total capability query timeout")})))
	for i := range commands {
		switch commands[i].Name {
		case "invite", "ssh-config", "status", "stop", "revoke", "cleanup", "relay":
			commands[i].Flags = specFlags(commands[i].Flags, jsonSpecFlag)
		}
	}
	return commands
}
func transferSpecFlags() []flagSpec {
	return specFlags(interfaceSpecFlag, retrySpecFlags, jsonSpecFlag, []flagSpec{
		specFlag("timeout", "duration", "5m0s", "Maximum transfer duration"),
		specFlag("recursive", "bool", "false", "Transfer a tree; mutually exclusive with resume"),
		specFlag("exclude", "string[]", "", "Relative slash glob or basename exclusion; no **"),
		specFlag("max-files", "int", "10000", "Recursive file limit; 1..100000"),
		specFlag("max-depth", "int", "32", "Recursive nesting limit; 0..128"),
		specFlag("resume", "bool", "false", "Resume one file using a private journal and owned partial"),
		specFlag("resume-state", "string", "", "Private resume journal path; requires resume"),
		specFlag("progress", "string", "", "jsonl emits progress events on stderr"),
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
