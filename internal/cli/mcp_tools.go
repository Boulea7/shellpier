package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type mcpTool struct {
	Name                           string         `json:"name"`
	Description                    string         `json:"description"`
	InputSchema                    map[string]any `json:"inputSchema"`
	Annotations                    map[string]any `json:"annotations"`
	Meta                           map[string]any `json:"_meta"`
	command                        []string
	spec                           commandSpec
	ticket                         bool
	minPositionals, maxPositionals int
}

// Only explicitly reviewed, bounded command forms become MCP tools. New registry
// entries remain disabled until their stream and lifetime contracts are reviewed.
func mcpTools() []mcpTool {
	result := []mcpTool{}
	for _, spec := range commandSpecs {
		path := []string{spec.Name}
		if len(spec.Subcommands) > 0 {
			for _, sub := range spec.Subcommands {
				if tool, ok := mcpMakeTool([]string{spec.Name, sub.Name}, sub); ok {
					result = append(result, tool)
				}
			}
			continue
		}
		if tool, ok := mcpMakeTool(path, spec); ok {
			result = append(result, tool)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func mcpMakeTool(path []string, spec commandSpec) (mcpTool, bool) {
	name := strings.Join(path, "_")
	ticket := false
	min, max := 0, 0
	readOnly := false
	effects := append([]string(nil), spec.Effects...)
	switch path[0] {
	case "schema":
		max = 1
		readOnly = true
	case "version", "sessions", "status", "inspect", "doctor":
		readOnly = true
		ticket = path[0] == "inspect"
	case "check", "capabilities":
		ticket = true
		readOnly = true
	case "exec":
		ticket = true
	case "upload", "download":
		ticket = true
		min, max = 2, 2
	case "stop", "revoke", "cleanup", "ssh-config", "keygen":
		ticket = path[0] == "revoke" || path[0] == "ssh-config"
	case "files":
		if len(path) != 2 {
			return mcpTool{}, false
		}
		ticket = true
		min, max = 1, 1
		switch path[1] {
		case "list", "stat", "read", "checksum":
			readOnly = true
		case "write", "rename":
			min, max = 2, 2
		case "mkdir", "remove":
		default:
			return mcpTool{}, false
		}
	case "terminal":
		if len(path) != 2 {
			return mcpTool{}, false
		}
		switch path[1] {
		case "open":
			ticket = true
		case "read", "status", "wait", "operation":
			readOnly = true
		case "write", "resize", "signal", "close":
		default:
			return mcpTool{}, false
		}
		if path[1] != "open" {
			effects = []string{"local_read", "loopback_control"}
			if !readOnly {
				effects = append(effects, "remote_write", "local_write")
			}
		}
	case "jobs":
		if len(path) != 2 {
			return mcpTool{}, false
		}
		ticket = true
		switch path[1] {
		case "start":
			effects = []string{"network", "remote_exec", "remote_write"}
		case "status", "logs", "wait", "list", "operation":
			readOnly = true
		case "cancel":
			effects = []string{"network", "remote_write"}
		default:
			return mcpTool{}, false
		}
		// Job identity is explicit, never inferred from a previous tool invocation.
		if path[1] != "start" && path[1] != "list" {
			min, max = 1, 1
		}
	default:
		return mcpTool{}, false
	}
	properties := map[string]any{}
	required := []string{}
	if ticket {
		properties["ticket"] = map[string]any{"type": "string", "minLength": 2, "maxLength": 4096, "description": "Explicit @FILE access/host ticket reference or target:NAME. Never inline a credential.", "pattern": "^(@.+|target:.+)$"}
		required = append(required, "ticket")
	}
	if max > 0 {
		properties["positionals"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 65536}, "minItems": min, "maxItems": max, "description": "Positional arguments after the ticket, in the order shown by usage."}
		if min > 0 {
			required = append(required, "positionals")
		}
	}
	options := map[string]any{}
	for _, flag := range spec.Flags {
		if flag.Name == "json" || flag.Name == "no-stdin" {
			continue
		}
		property := map[string]any{"description": flag.Description}
		switch flag.Type {
		case "bool":
			property["type"] = "boolean"
		case "int", "int64", "uint64":
			property["type"] = "integer"
		case "string[]":
			property["type"] = "array"
			property["items"] = map[string]any{"type": "string", "maxLength": 65536}
			property["maxItems"] = 64
		default:
			property["type"] = "string"
			property["maxLength"] = 65536
		}
		options[flag.Name] = property
	}
	properties["options"] = map[string]any{"type": "object", "properties": options, "additionalProperties": false, "description": "CLI option values, without leading dashes. Stdin is always closed; tool calls have a total timeout."}
	if path[0] == "jobs" && len(path) > 1 && path[1] == "start" {
		properties["options"].(map[string]any)["required"] = []string{"operation-id"}
		required = append(required, "options")
	}
	description := spec.Summary + ". Usage: " + spec.Usage + ". Effects: " + strings.Join(effects, ", ") + ". Output: " + spec.Output + "."
	if path[0] == "exec" || (path[0] == "jobs" && path[1] == "start") {
		description += " A timeout or canceled response does not prove that execution or submission did not occur. Check state before retrying; never automatically replay."
	}
	return mcpTool{Name: "shellpier_" + name, Description: description, InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, Annotations: map[string]any{"readOnlyHint": readOnly, "destructiveHint": !readOnly, "idempotentHint": readOnly, "openWorldHint": ticket || path[0] == "doctor"}, Meta: map[string]any{"io.shellpier/effects": effects}, command: path, spec: spec, ticket: ticket, minPositionals: min, maxPositionals: max}, true
}

func (t mcpTool) arguments(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var input struct {
		Ticket      string         `json:"ticket"`
		Positionals []string       `json:"positionals"`
		Options     map[string]any `json:"options"`
	}
	if decoder.Decode(&input) != nil {
		return nil, errors.New("Invalid tool argument object; consult inputSchema")
	}
	if t.ticket {
		if len(input.Ticket) > 4096 || strings.ContainsAny(input.Ticket, "\x00\r\n") || !(strings.HasPrefix(input.Ticket, "@") && len(input.Ticket) > 1 || strings.HasPrefix(input.Ticket, "target:") && len(input.Ticket) > 7) {
			return nil, errors.New("Provide an explicit @FILE or target:NAME ticket reference")
		}
	} else if input.Ticket != "" {
		return nil, errors.New("This tool does not accept a ticket")
	}
	if len(input.Positionals) < t.minPositionals || len(input.Positionals) > t.maxPositionals {
		return nil, errors.New("Incorrect number of positional arguments; consult tool usage")
	}
	for _, value := range input.Positionals {
		if len(value) > 65536 || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "-") {
			return nil, errors.New("Invalid positional argument")
		}
	}
	if t.command[0] == "jobs" && len(t.command) > 1 && t.command[1] == "start" {
		id, ok := input.Options["operation-id"].(string)
		if !ok || strings.TrimSpace(id) == "" {
			return nil, errors.New("jobs start requires an explicit stable operation-id; preserve it if submission is uncertain")
		}
	}
	args := append([]string(nil), t.command...)
	args = append(args, "--json")
	keys := make([]string, 0, len(input.Options))
	for key := range input.Options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var flag *flagSpec
		for i := range t.spec.Flags {
			if t.spec.Flags[i].Name == key {
				flag = &t.spec.Flags[i]
				break
			}
		}
		if flag == nil || key == "json" || key == "no-stdin" {
			return nil, errors.New("Unknown or reserved tool option")
		}
		values, ok := mcpFlagValue(input.Options[key], flag.Type)
		if !ok || len(values) > 64 {
			return nil, errors.New("Tool option has an invalid type")
		}
		for _, value := range values {
			if len(value) > 65536 || strings.ContainsRune(value, 0) {
				return nil, errors.New("Tool option exceeds its input limit")
			}
			if key == "file" && value == "-" {
				return nil, errors.New("MCP tools cannot read from the protocol input stream")
			}
			args = append(args, "--"+key+"="+value)
		}
	}
	if t.command[0] == "exec" {
		args = append(args, "--no-stdin")
	}
	if t.ticket {
		args = append(args, input.Ticket)
	}
	args = append(args, input.Positionals...)
	if t.command[0] == "exec" {
		if _, command := input.Options["command"]; !command {
			if _, script := input.Options["script"]; !script {
				return nil, errors.New("exec requires an explicit command or script")
			}
		}
	}
	return args, nil
}
