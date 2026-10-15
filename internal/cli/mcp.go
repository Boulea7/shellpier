package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// This transport supports stateless MCP 2026 requests and MCP 2025 initialization.
const mcpProtocolVersion = "2026-07-28"
const mcpLegacyVersion = "2025-11-25"
const mcpMaxMessage = 1 << 20

type mcpOptions struct {
	timeout                  time.Duration
	concurrency, outputLimit int
	version                  string
}
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Modern  bool            `json:"-"`
}
type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}
type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}
type mcpCall struct {
	cancel   context.CancelFunc
	canceled bool
}
type mcpRunner func(context.Context, []string, io.Reader, io.Writer, io.Writer, string) error

type mcpServer struct {
	ctx                context.Context
	cancel             context.CancelFunc
	options            mcpOptions
	output             io.Writer
	run                mcpRunner
	mu                 sync.Mutex
	writeMu            sync.Mutex
	workers            sync.WaitGroup
	active             map[string]*mcpCall
	initialized, ready bool
	writeErr           error
	tools              []mcpTool
}

func mcp(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, "Usage: shellpier mcp serve [--timeout 60s] [--max-concurrent 4] [--max-output 262144]\nServe MCP tools over stdio. Supports protocol 2026-07-28 and 2025-11-25 initialization.\n")
		return err
	}
	if args[0] != "serve" {
		return &UsageError{Err: errors.New("usage: shellpier mcp serve [--timeout 60s] [--max-concurrent 4] [--max-output 262144]")}
	}
	f := flags("mcp serve", stderr)
	timeout := f.Duration("timeout", time.Minute, "maximum duration of each tool call; at most 5m")
	concurrent := f.Int("max-concurrent", 4, "maximum concurrent tool calls; 1..16")
	output := f.Int("max-output", 256*1024, "maximum captured tool output bytes; 1024..1048576")
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || *timeout <= 0 || *timeout > 5*time.Minute || *concurrent < 1 || *concurrent > 16 || *output < 1024 || *output > 1<<20 {
		return &UsageError{Err: errors.New("invalid MCP server limits")}
	}
	return serveMCP(ctx, stdin, stdout, mcpOptions{*timeout, *concurrent, *output, version}, Run)
}

type mcpLine struct {
	data []byte
	err  error
}

func serveMCP(parent context.Context, input io.Reader, output io.Writer, options mcpOptions, run mcpRunner) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s := &mcpServer{ctx: ctx, cancel: cancel, options: options, output: output, run: run, active: map[string]*mcpCall{}, tools: mcpTools()}
	// Closing our input on shutdown unblocks a stdio pipe without abandoning reads.
	stopClose := func() bool { return false }
	if closer, ok := input.(io.ReadCloser); ok {
		stopClose = context.AfterFunc(ctx, func() { closer.Close() })
	}
	defer stopClose()
	if closer, ok := output.(io.Closer); ok {
		stopOutput := context.AfterFunc(ctx, func() { closer.Close() })
		defer stopOutput()
	}
	lines := make(chan mcpLine, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), mcpMaxMessage+1)
		for scanner.Scan() {
			data := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- mcpLine{data: data}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- mcpLine{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	defer func() { cancel(); s.workers.Wait() }()
	for {
		select {
		case <-ctx.Done():
			s.writeMu.Lock()
			err := s.writeErr
			s.writeMu.Unlock()
			if err != nil {
				return err
			}
			return parent.Err()
		case line, ok := <-lines:
			if !ok {
				s.writeMu.Lock()
				err := s.writeErr
				s.writeMu.Unlock()
				return err
			}
			if line.err != nil {
				s.send(nil, nil, &mcpRPCError{Code: -32700, Message: "Input message exceeds the limit or could not be read"})
				return errors.New("MCP input stream failed or exceeded the message limit")
			}
			if len(line.data) > mcpMaxMessage {
				s.send(nil, nil, &mcpRPCError{Code: -32700, Message: "Input message exceeds the limit"})
				return errors.New("MCP input message exceeds limit")
			}
			s.receive(line.data)
		}
	}
}

func mcpID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if len(text) > 256 {
			return "", false
		}
		return "s:" + text, true
	}
	if len(raw) > 40 {
		return "", false
	}
	for i, b := range raw {
		if b == '-' && i == 0 {
			continue
		}
		if b < '0' || b > '9' {
			return "", false
		}
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return "", false
	}
	if string(raw) == "-0" {
		return "n:0", true
	}
	return "n:" + string(raw), true
}

func (s *mcpServer) send(id json.RawMessage, result any, problem *mcpRPCError) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.writeMu.Lock()
	err := json.NewEncoder(s.output).Encode(mcpResponse{"2.0", id, result, problem})
	if err != nil && s.writeErr == nil {
		s.writeErr = err
	}
	s.writeMu.Unlock()
	if err != nil {
		s.cancel()
	}

}

func (s *mcpServer) respond(request mcpRequest, result any, problem *mcpRPCError) {
	if request.Modern && result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			s.send(request.ID, nil, &mcpRPCError{Code: -32603, Message: "Cannot encode result"})
			return
		}
		var data map[string]any
		if json.Unmarshal(b, &data) != nil {
			s.send(request.ID, nil, &mcpRPCError{Code: -32603, Message: "Invalid result object"})
			return
		}
		data["resultType"] = "complete"
		data["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": "shellpier", "version": s.options.version}}
		result = data
	}
	s.send(request.ID, result, problem)
}

func (s *mcpServer) modern(request *mcpRequest) bool {
	var params map[string]json.RawMessage
	json.Unmarshal(request.Params, &params)
	raw, hasMeta := params["_meta"]
	if !hasMeta && s.ready {
		return true
	}
	if !mcpObject(raw) {
		s.respond(*request, nil, &mcpRPCError{Code: -32602, Message: "Modern requests require per-request _meta"})
		return false
	}
	var meta map[string]json.RawMessage
	json.Unmarshal(raw, &meta)
	if _, modernVersion := meta["io.modelcontextprotocol/protocolVersion"]; !modernVersion && s.ready {
		return true
	}
	var version string
	if json.Unmarshal(meta["io.modelcontextprotocol/protocolVersion"], &version) != nil || version == "" || !mcpObject(meta["io.modelcontextprotocol/clientCapabilities"]) {
		s.respond(*request, nil, &mcpRPCError{Code: -32602, Message: "Missing required protocolVersion or clientCapabilities metadata"})
		return false
	}
	request.Modern = true
	if version != mcpProtocolVersion {
		s.respond(*request, nil, &mcpRPCError{Code: -32022, Message: "Unsupported protocol version", Data: map[string]any{"supported": []string{mcpProtocolVersion, mcpLegacyVersion}, "requested": version}})
		return false
	}
	return true
}

func mcpObject(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '{' && json.Valid(raw)
}

func (s *mcpServer) receive(data []byte) {
	if !utf8.Valid(data) || !json.Valid(data) {
		s.send(nil, nil, &mcpRPCError{Code: -32700, Message: "Invalid JSON message"})
		return
	}
	if !mcpObject(data) {
		s.send(nil, nil, &mcpRPCError{Code: -32600, Message: "Expected one JSON-RPC object"})
		return
	}
	var request mcpRequest
	if json.Unmarshal(data, &request) != nil || request.JSONRPC != "2.0" || request.Method == "" {
		s.send(nil, nil, &mcpRPCError{Code: -32600, Message: "Invalid JSON-RPC request"})
		return
	}
	key, validID := mcpID(request.ID)
	if len(request.ID) == 0 {
		if request.Method == "notifications/initialized" && s.initialized {
			s.ready = true
		}
		if request.Method == "notifications/cancelled" {
			var p struct {
				ID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(request.Params, &p) == nil {
				if key, ok := mcpID(p.ID); ok {
					s.mu.Lock()
					if call := s.active[key]; call != nil {
						call.canceled = true
						call.cancel()
					}
					s.mu.Unlock()
				}
			}
		}
		return
	}
	if !validID {
		s.send(nil, nil, &mcpRPCError{Code: -32600, Message: "Request id must be a string or integer"})
		return
	}
	if request.Params != nil && !mcpObject(request.Params) {
		s.respond(request, nil, &mcpRPCError{Code: -32602, Message: "Parameters must be an object"})
		return
	}
	if request.Method == "initialize" {
		var p struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		}
		json.Unmarshal(request.Params, &p)
		if _, modernVersion := p.Meta["io.modelcontextprotocol/protocolVersion"]; modernVersion {
			if s.modern(&request) {
				s.respond(request, nil, &mcpRPCError{Code: -32601, Message: "initialize is not part of the selected protocol"})
			}
			return
		}
		s.initialize(request)
		return
	}
	if request.Method == "ping" && !s.ready {
		var p map[string]json.RawMessage
		json.Unmarshal(request.Params, &p)
		if _, ok := p["_meta"]; !ok {
			s.respond(request, map[string]any{}, nil)
			return
		}
	}
	if !s.modern(&request) {
		return
	}
	if request.Method == "server/discover" {
		if !request.Modern {
			s.respond(request, nil, &mcpRPCError{Code: -32602, Message: "Discovery requires modern protocol metadata"})
			return
		}
		s.respond(request, map[string]any{"supportedVersions": []string{mcpProtocolVersion, mcpLegacyVersion}, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": "Bounded ShellPier CLI tools. Explicit @FILE or target:NAME references are required for remote operations; cancellation never rolls back effects."}, nil)
		return
	}
	if request.Method == "ping" && !request.Modern {
		s.respond(request, map[string]any{}, nil)
		return
	}
	switch request.Method {
	case "tools/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		if len(request.Params) > 0 && json.Unmarshal(request.Params, &p) != nil || p.Cursor != "" {
			s.respond(request, nil, &mcpRPCError{Code: -32602, Message: "Invalid tools cursor"})
			return
		}
		s.respond(request, map[string]any{"tools": s.tools}, nil)
	case "tools/call":
		s.startCall(key, request)
	default:
		s.respond(request, nil, &mcpRPCError{Code: -32601, Message: "Method not found"})
	}
}

func (s *mcpServer) initialize(request mcpRequest) {
	if s.initialized {
		s.respond(request, nil, &mcpRPCError{Code: -32600, Message: "Server is already initialized"})
		return
	}
	var p struct {
		Protocol     string          `json:"protocolVersion"`
		Capabilities json.RawMessage `json:"capabilities"`
		Client       struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(request.Params, &p) != nil || p.Protocol == "" || !mcpObject(p.Capabilities) || p.Client.Name == "" || p.Client.Version == "" {
		s.respond(request, nil, &mcpRPCError{Code: -32602, Message: "Initialization requires protocolVersion, capabilities and clientInfo"})
		return
	}
	version := mcpLegacyVersion

	s.initialized = true
	s.respond(request, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "shellpier", "version": s.options.version}, "instructions": "Tools use explicit ticket or local state references. Execution and file operations may change remote data. Responses are bounded; cancellation does not roll back effects. Supports MCP 2026 per-request metadata and MCP 2025 initialization."}, nil)
}

func (s *mcpServer) startCall(key string, request mcpRequest) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Task      json.RawMessage `json:"task"`
	}
	if json.Unmarshal(request.Params, &p) != nil || p.Name == "" || (p.Arguments != nil && !mcpObject(p.Arguments)) {
		s.respond(request, nil, &mcpRPCError{Code: -32602, Message: "Invalid tool call"})
		return
	}
	if len(p.Task) > 0 {
		s.respond(request, nil, &mcpRPCError{Code: -32601, Message: "Task-augmented calls are not supported"})
		return
	}
	var selected *mcpTool
	for i := range s.tools {
		if s.tools[i].Name == p.Name {
			selected = &s.tools[i]
			break
		}
	}
	if selected == nil {
		s.respond(request, nil, &mcpRPCError{Code: -32602, Message: "Unknown or disabled tool"})
		return
	}
	args, err := selected.arguments(p.Arguments)
	if err != nil {
		s.respond(request, mcpToolFailure("usage", err.Error()), nil)
		return
	}
	s.mu.Lock()
	if s.active[key] != nil {
		s.mu.Unlock()
		s.respond(request, nil, &mcpRPCError{Code: -32600, Message: "Request id is already in use"})
		return
	}
	if len(s.active) >= s.options.concurrency {
		s.mu.Unlock()
		s.respond(request, nil, &mcpRPCError{Code: -32901, Message: "Concurrent tool limit reached"})
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.options.timeout)
	call := &mcpCall{cancel: cancel}
	s.active[key] = call
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		defer cancel()
		result := s.execute(ctx, *selected, args)
		// Serializing cancellation and publication prevents responses after accepted cancellation.
		s.mu.Lock()
		if !call.canceled && s.ctx.Err() == nil {
			s.respond(request, result, nil)
		}
		delete(s.active, key)
		s.mu.Unlock()

	}()
}

type mcpToolResult struct {
	Content    []mcpText      `json:"content"`
	Structured map[string]any `json:"structuredContent"`
	IsError    bool           `json:"isError"`
}
type mcpText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func mcpResult(data map[string]any, failed bool) mcpToolResult {
	b, _ := json.Marshal(data)
	return mcpToolResult{[]mcpText{{"text", string(b)}}, data, failed}
}
func mcpToolFailure(code, message string) mcpToolResult {
	return mcpResult(map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}}, true)
}

type mcpCapture struct {
	data      []byte
	limit     int
	truncated bool
}

func (w *mcpCapture) Write(p []byte) (int, error) {
	remaining := w.limit - len(w.data)
	if len(p) > remaining {
		w.truncated = true
		w.data = append(w.data, p[:remaining]...)
	} else {
		w.data = append(w.data, p...)
	}
	return len(p), nil
}

func (s *mcpServer) execute(ctx context.Context, tool mcpTool, args []string) mcpToolResult {
	credentials := mcpCredentials(args)
	output := &mcpCapture{limit: s.options.outputLimit}
	err := s.run(ctx, args, strings.NewReader(""), output, io.Discard, s.options.version)
	var unknown *JobStartUnknownError
	unknownStart := errors.As(err, &unknown)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	output.data = mcpRedact(output.data, credentials)
	result := map[string]any{"ok": err == nil, "command": strings.Join(tool.command, " "), "truncated": output.truncated}
	if output.truncated {
		result["output_text"] = strings.ToValidUTF8(string(output.data), "�")
	} else if len(bytes.TrimSpace(output.data)) > 0 {
		var parsed any
		if json.Unmarshal(output.data, &parsed) == nil {
			result["output"] = parsed
		} else {
			result["output_text"] = strings.ToValidUTF8(string(output.data), "�")
		}
	}
	if err != nil {
		detail := classifyJSONError(err)
		result["error"] = detail
		if tool.command[0] == "jobs" && len(tool.command) > 1 && tool.command[1] == "start" && (unknownStart || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			result["outcome"] = "unknown"
			if unknownStart {
				result["operation_id"] = unknown.OperationID
				result["error"] = map[string]any{"code": "outcome_unknown", "message": "Job submission may have succeeded. Preserve operation_id and inspect or retry the same operation ID and content; do not submit a new operation."}
			}
			result["retry_safe"] = false
		}
	}
	return mcpResult(result, err != nil)
}

// mcpFlagValue converts a validated schema value into one CLI argument.
func mcpFlagValue(value any, kind string) ([]string, bool) {
	switch kind {
	case "bool":
		v, ok := value.(bool)
		if ok {
			return []string{strconv.FormatBool(v)}, true
		}
	case "uint64":
		v, ok := value.(json.Number)
		if ok {
			if _, err := strconv.ParseUint(string(v), 10, 64); err == nil {
				return []string{string(v)}, true
			}
		}
	case "int", "int64":
		v, ok := value.(json.Number)
		if ok {
			if _, err := strconv.ParseInt(string(v), 10, 64); err == nil {
				return []string{string(v)}, true
			}
		}
	case "string[]":
		values, ok := value.([]any)
		if ok {
			result := make([]string, 0, len(values))
			for _, v := range values {
				text, ok := v.(string)
				if !ok {
					return nil, false
				}
				result = append(result, text)
			}
			return result, true
		}
	default:
		v, ok := value.(string)
		if ok {
			return []string{v}, true
		}
	}
	return nil, false
}
