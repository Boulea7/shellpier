package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type mcpTestClient struct {
	input  *io.PipeWriter
	output *bufio.Reader
	done   <-chan error
	cancel context.CancelFunc
}

func startMCPTest(t *testing.T, run mcpRunner, options mcpOptions) *mcpTestClient {
	t.Helper()
	reader, input := io.Pipe()
	output, writer := io.Pipe()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { err := serveMCP(ctx, reader, writer, options, run); writer.Close(); done <- err }()
	client := &mcpTestClient{input, bufio.NewReader(output), done, cancel}
	t.Cleanup(func() {
		cancel()
		input.Close()
		output.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("MCP server did not stop")
		}
	})
	return client
}
func (c *mcpTestClient) send(t *testing.T, message any) {
	t.Helper()
	b, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.input.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}
func (c *mcpTestClient) read(t *testing.T) map[string]any {
	t.Helper()
	type answer struct {
		data []byte
		err  error
	}
	ready := make(chan answer, 1)
	go func() { data, err := c.output.ReadBytes('\n'); ready <- answer{data, err} }()
	select {
	case response := <-ready:
		if response.err != nil {
			t.Fatal(response.err)
		}
		var value map[string]any
		if err := json.Unmarshal(response.data, &value); err != nil {
			t.Fatalf("non-protocol stdout %q: %v", response.data, err)
		}
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("MCP response timeout")
	}
	return nil
}
func mcpModernRequest(id any, method string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": mcpProtocolVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}
func mcpTestOptions() mcpOptions { return mcpOptions{time.Second, 2, 1024, "test"} }
func mcpCallRequest(id any, name string, args any) map[string]any {
	return mcpModernRequest(id, "tools/call", map[string]any{"name": name, "arguments": args})
}
func mcpResultObject(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", response)
	}
	return result
}

func TestMCPModernDiscoveryAndLegacyInitialization(t *testing.T) {
	c := startMCPTest(t, Run, mcpTestOptions())
	c.send(t, mcpModernRequest("discover", "server/discover", nil))
	response := c.read(t)
	result := mcpResultObject(t, response)
	if response["id"] != "discover" || result["resultType"] != "complete" || result["supportedVersions"] == nil {
		t.Fatalf("invalid discovery: %v", response)
	}
	c.send(t, mcpModernRequest("modern-init", "initialize", nil))
	if c.read(t)["error"] == nil {
		t.Fatal("modern protocol accepted an initialize handshake")
	}
	c.send(t, mcpModernRequest(22, "tools/list", nil))
	result = mcpResultObject(t, c.read(t))
	tools := result["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	for _, item := range tools {
		tool := item.(map[string]any)
		name := tool["name"].(string)
		for _, forbidden := range []string{"shellpier_share", "shellpier_host", "shellpier_relay", "shellpier_proxy", "shellpier_shell", "shellpier_mcp", "shellpier_service", "shellpier_target", "shellpier_forward", "shellpier_terminal_tail"} {
			if name == forbidden || strings.HasPrefix(name, forbidden+"_") {
				t.Fatalf("unsafe tool exposed: %s", name)
			}
		}
	}
	c.send(t, mcpCallRequest("version", "shellpier_version", map[string]any{}))
	response = c.read(t)
	result = mcpResultObject(t, response)
	if result["isError"] != false || response["id"] != "version" {
		t.Fatalf("version failed: %v", response)
	}
	c.send(t, map[string]any{"jsonrpc": "2.0", "id": 30, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}})
	result = mcpResultObject(t, c.read(t))
	if result["protocolVersion"] != mcpLegacyVersion {
		t.Fatal("legacy negotiation failed")
	}
	c.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	c.send(t, map[string]any{"jsonrpc": "2.0", "id": "legacy", "method": "tools/list", "params": map[string]any{"_meta": map[string]any{"progressToken": "legacy-progress-token"}}})
	response = c.read(t)
	result = mcpResultObject(t, response)
	if result["resultType"] != nil || response["id"] != "legacy" {
		t.Fatalf("legacy result envelope changed: %v", response)
	}
}

func TestMCPInvalidRequestsAndArguments(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	runner := func(context.Context, []string, io.Reader, io.Writer, io.Writer, string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}
	c := startMCPTest(t, runner, mcpTestOptions())
	for _, line := range []string{"{bad}", "[]", `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`} {
		c.input.Write([]byte(line + "\n"))
		response := c.read(t)
		if response["error"] == nil {
			t.Fatalf("invalid request accepted: %s", line)
		}
	}
	request := mcpModernRequest(2, "server/discover", nil)
	request["params"].(map[string]any)["_meta"].(map[string]any)["io.modelcontextprotocol/protocolVersion"] = "future-version"
	c.send(t, request)
	response := c.read(t)
	if response["error"].(map[string]any)["code"] != float64(-32022) {
		t.Fatal("unsupported version not reported")
	}
	for _, arguments := range []map[string]any{{}, {"ticket": "inline-private-ticket", "options": map[string]any{"command": "whoami"}}, {"ticket": "@missing", "options": map[string]any{"json": false, "command": "whoami"}}, {"ticket": "@missing", "options": map[string]any{"command": 123}}, {"ticket": "@missing", "options": map[string]any{"command": "whoami", "not-a-real-flag": "secret"}}, {"ticket": "@missing", "positionals": []string{"--help"}, "options": map[string]any{"command": "whoami"}}} {
		c.send(t, mcpCallRequest("bad", "shellpier_exec", arguments))
		result := mcpResultObject(t, c.read(t))
		if result["isError"] != true {
			t.Fatalf("invalid arguments accepted: %v", arguments)
		}
	}
	c.send(t, mcpCallRequest("disabled", "shellpier_shell", map[string]any{}))
	if c.read(t)["error"] == nil {
		t.Fatal("disabled tool accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatal("invalid input executed a command")
	}
}

func TestMCPCancellationConcurrencyTimeoutAndTruncation(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	runner := func(ctx context.Context, args []string, _ io.Reader, out, diagnostic io.Writer, _ string) error {
		if args[0] == "version" {
			out.Write([]byte(strings.Repeat("x", 4096)))
			diagnostic.Write([]byte("private-diagnostic-token"))
			return nil
		}
		started <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		return ctx.Err()
	}
	options := mcpTestOptions()
	options.concurrency = 1
	options.timeout = 150 * time.Millisecond
	c := startMCPTest(t, runner, options)
	c.send(t, mcpCallRequest("cancel-me", "shellpier_exec", map[string]any{"ticket": "@local-fixture", "options": map[string]any{"command": "never executed by fake runner"}}))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("call not started")
	}
	c.send(t, mcpCallRequest("overflow", "shellpier_version", map[string]any{}))
	if c.read(t)["error"] == nil {
		t.Fatal("concurrency limit ignored")
	}
	c.send(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": "cancel-me", "reason": "do not log this private string"}})
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("call was not canceled")
	}
	// Barrier: no response for the canceled request may precede this discovery reply.
	c.send(t, mcpModernRequest("barrier", "server/discover", nil))
	if c.read(t)["id"] != "barrier" {
		t.Fatal("canceled request produced a response")
	}
	c.send(t, mcpCallRequest("truncated", "shellpier_version", map[string]any{}))
	response := c.read(t)
	structured := mcpResultObject(t, response)["structuredContent"].(map[string]any)
	if structured["truncated"] != true || len(structured["output_text"].(string)) != 1024 {
		t.Fatalf("output not bounded: %v", response)
	}
	c.send(t, mcpCallRequest("timeout", "shellpier_exec", map[string]any{"ticket": "@local-fixture", "options": map[string]any{"command": "never executed"}}))
	<-started
	response = c.read(t)
	structured = mcpResultObject(t, response)["structuredContent"].(map[string]any)
	if structured["error"].(map[string]any)["code"] != "timeout" {
		t.Fatalf("timeout not classified: %v", response)
	}
	if strings.Contains(fmtJSON(response), "private-diagnostic-token") {
		t.Fatal("diagnostic leaked")
	}
}
func fmtJSON(value any) string { data, _ := json.Marshal(value); return string(data) }

func TestMCPInputLimitAndSafeErrors(t *testing.T) {
	c := startMCPTest(t, Run, mcpTestOptions())
	writeDone := make(chan error, 1)
	go func() {
		_, err := c.input.Write([]byte(strings.Repeat("a", mcpMaxMessage+10) + "\n"))
		writeDone <- err
	}()
	response := c.read(t)
	if response["error"] == nil {
		t.Fatal("oversized request accepted")
	}
	if err := <-writeDone; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	c = startMCPTest(t, Run, mcpTestOptions())
	c.send(t, mcpCallRequest("missing", "shellpier_exec", map[string]any{"ticket": "@secret-named-missing-ticket", "options": map[string]any{"command": "whoami"}}))
	response = c.read(t)
	if strings.Contains(fmtJSON(response), "secret-named") || mcpResultObject(t, response)["isError"] != true {
		t.Fatalf("unsafe error: %v", response)
	}
}

func TestMCPStdIOBlackBox(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "shellpier-mcp-test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	started := make(chan struct{}, 1)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer local.Close()
	fixture := t.TempDir()
	writeSessionFixture(t, fixture, localState{Version: 1, Kind: "shellpier-host", ID: "mcp_fixture_session_id", Secret: strings.Repeat("a", 64), ControlURL: local.URL})
	build := exec.Command("go", "build", "-o", binary, "../../cmd/shellpier")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "mcp", "serve", "--timeout", "1s", "--max-output", "1024")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic mcpTestLog
	command.Stderr = &diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	writer := json.NewEncoder(input)
	reader := bufio.NewReader(output)
	for _, request := range []map[string]any{mcpModernRequest(1, "server/discover", nil), mcpCallRequest("stdio-version", "shellpier_version", map[string]any{})} {
		if err := writer.Encode(request); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v stderr=%s", err, diagnostic.String())
		}
		var response map[string]any
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("stdout polluted: %s", line)
		}
		if response["error"] != nil {
			t.Fatalf("black-box protocol error: %s", line)
		}
	}
	if err := writer.Encode(mcpCallRequest("schema-truncated", "shellpier_schema", map[string]any{})); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	structured := mcpResultObject(t, response)["structuredContent"].(map[string]any)
	if structured["truncated"] != true {
		t.Fatalf("subprocess output limit ignored: %s", line)
	}
	if _, err := input.Write([]byte("{invalid}\n")); err != nil {
		t.Fatal(err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(line, &response); err != nil || response["error"] == nil {
		t.Fatalf("malformed request not rejected: %s", line)
	}
	if err := writer.Encode(mcpCallRequest("cancel-stdio", "shellpier_sessions", map[string]any{"options": map[string]any{"dir": fixture}})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("local fixture probe did not start")
	}
	writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": "cancel-stdio"}})
	writer.Encode(mcpModernRequest("after-cancel", "server/discover", nil))
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(line, &response); err != nil || response["id"] != "after-cancel" {
		t.Fatalf("canceled subprocess call replied: %s", line)
	}
	input.Close()
	if err := command.Wait(); err != nil {
		t.Fatalf("MCP subprocess exit: %v stderr=%s", err, diagnostic.String())
	}
	if diagnostic.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", diagnostic.String())
	}
}

type mcpTestLog struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (w *mcpTestLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.Write(p)
}
func (w *mcpTestLog) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.data.String() }
func (w *mcpTestLog) Len() int       { w.mu.Lock(); defer w.mu.Unlock(); return w.data.Len() }

func TestMCPExplicitJobOperationIDAndSafeFailure(t *testing.T) {
	spec := commandSpec{Name: "start", Summary: "Start job", Usage: "shellpier jobs start TICKET", Flags: []flagSpec{{Name: "command", Type: "string"}, {Name: "operation-id", Type: "string"}}}
	tool, ok := mcpMakeTool([]string{"jobs", "start"}, spec)
	if !ok {
		t.Fatal("job tool missing")
	}
	if _, err := tool.arguments(json.RawMessage(`{"ticket":"target:fixture","options":{"command":"no execution"}}`)); err == nil {
		t.Fatal("job start accepted without stable operation id")
	}
	args, err := tool.arguments(json.RawMessage(`{"ticket":"target:fixture","options":{"command":"no execution","operation-id":"0123456789abcdef0123456789abcdef"}}`))
	if err != nil {
		t.Fatal(err)
	}
	server := mcpServer{options: mcpTestOptions(), run: func(context.Context, []string, io.Reader, io.Writer, io.Writer, string) error {
		return &JobStartUnknownError{OperationID: "0123456789abcdef0123456789abcdef", Err: errors.New("private raw diagnostic")}
	}}
	result := server.execute(context.Background(), tool, args)
	if !result.IsError || result.Structured["outcome"] != "unknown" || result.Structured["retry_safe"] != false || result.Structured["operation_id"] != "0123456789abcdef0123456789abcdef" || strings.Contains(fmtJSON(result), "private raw diagnostic") {
		t.Fatalf("unsafe unknown-start result: %+v", result)
	}
}

func TestMCPKnownSecretRedaction(t *testing.T) {
	secret := "-----BEGIN PRIVATE KEY-----\nfixture-private-value\n-----END PRIVATE KEY-----"
	input, _ := json.Marshal(map[string]any{"stdout": secret, "secret": "control-secret", "nested": map[string]any{"relay_token": "credential"}})
	output := string(mcpRedact(input, []string{secret}))
	if strings.Contains(output, "fixture-private") || strings.Contains(output, "control-secret") || strings.Contains(output, "credential") {
		t.Fatalf("credentials escaped redaction: %s", output)
	}
	partial := []byte("prefix" + secret[:32])
	if strings.Contains(string(mcpRedact(partial, []string{secret})), "BEGIN PRIVATE") {
		t.Fatal("truncated secret prefix leaked")
	}
}

func TestMCPJobOperationLookupTool(t *testing.T) {
	tool, ok := mcpMakeTool([]string{"jobs", "operation"}, commandSpec{Name: "operation", Summary: "Find a job by submission operation ID", Usage: "shellpier jobs operation TICKET OPERATION-ID"})
	if !ok || tool.Name != "shellpier_jobs_operation" || tool.Annotations["readOnlyHint"] != true {
		t.Fatalf("invalid operation lookup tool: %+v", tool)
	}
	args, err := tool.arguments(json.RawMessage(`{"ticket":"target:fixture","positionals":["0123456789abcdef0123456789abcdef"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "jobs operation --json target:fixture 0123456789abcdef0123456789abcdef" {
		t.Fatalf("invalid lookup arguments: %v", args)
	}
	if _, err := tool.arguments(json.RawMessage(`{"ticket":"target:fixture"}`)); err == nil {
		t.Fatal("lookup accepted without operation ID")
	}
}
