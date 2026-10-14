// Package cli implements the portable ShellPier command line.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/netutil"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/relay"
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

func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	return f
}

func tokenKey(path string) (string, error) {
	value := os.Getenv("SHELLPIER_RELAY_KEY")
	if path != "" {
		b, err := readRegular(path, 4096)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(string(b))
	}
	if len(value) < 32 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("relay key needs at least 32 characters; set SHELLPIER_RELAY_KEY or --token-file")
	}
	return value, nil
}

func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	return access.ReadFile(path, limit)
}

func displayArg(value string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writePrivate(path string, contents []byte) (result error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	created, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	defer func() {
		result = errors.Join(result, f.Close())
		if result != nil {
			if current, err := os.Lstat(path); err == nil && os.SameFile(created, current) {
				result = errors.Join(result, os.Remove(path))
			}
		}
	}()
	if err = platform.SecureFile(path); err != nil {
		return err
	}
	_, err = f.Write(contents)
	return err
}

func writeJSONPrivate(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

func ticketSession(t access.Ticket) relay.Session {
	s := relay.Session{ID: t.ID, ExpiresAt: t.ExpiresAt}
	if t.Kind == "host" {
		s.HostToken = t.RelayToken
	} else {
		s.ClientToken = t.RelayToken
	}
	return s
}

func connect(ctx context.Context, mode string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	f := flags(mode, stderr)
	iface := f.String("interface", "", "constrain direct TCP routing to this interface (macOS/Linux)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() < 1 {
		return errors.New("provide an access ticket or @file")
	}
	t, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err := t.Validate("access"); err != nil {
		return err
	}
	if mode == "proxy" {
		if f.NArg() != 1 {
			return errors.New("proxy accepts exactly one ticket")
		}
		return access.Proxy(ctx, t, *iface, stdin, stdout)
	}
	commands := f.Args()[1:]
	if len(commands) > 0 && commands[0] == "--" {
		commands = commands[1:]
	}
	command := strings.Join(commands, " ")
	if command == "" {
		command = "whoami"
	}
	client, err := access.Dial(ctx, t, *iface)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	return session.Run(command)
}

// ssh-config permits the natural "ticket --out directory" spelling.
func sshConfig(args []string, stdout, stderr io.Writer) error {
	var ticketArg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		ticketArg, args = args[0], args[1:]
	}
	f := flags("ssh-config", stderr)
	dir := f.String("out", "", "new private output directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if ticketArg == "" && f.NArg() == 1 {
		ticketArg = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("ssh-config accepts one ticket and --out directory")
	}
	if ticketArg == "" || *dir == "" {
		return errors.New("usage: shellpier ssh-config TICKET --out NEW-DIRECTORY")
	}
	t, err := access.Read(ticketArg)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := access.WriteConfig(t, *dir, exe)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "SSH config: %s\nUse: ssh -F %s shellpier whoami\n", path, displayArg(path))
	return nil
}

func invite(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("invite", stderr)
	base := f.String("relay", os.Getenv("SHELLPIER_RELAY_URL"), "HTTPS relay URL")
	keyFile := f.String("token-file", "", "relay enrollment key file")
	ttl := f.Duration("ttl", time.Hour, "authorization lifetime; 0 means explicitly persistent")
	dir := f.String("out", "", "new private output directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *base == "" || *dir == "" || *ttl < 0 || *ttl > 0 && *ttl < time.Second {
		return errors.New("usage: shellpier invite --relay URL --out NEW-DIRECTORY [--ttl 1h]")
	}
	key, err := tokenKey(*keyFile)
	if err != nil {
		return err
	}
	a, h, err := access.NewPair(*ttl)
	if err != nil {
		return err
	}
	createCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	s, err := relay.Create(createCtx, *base, key, *ttl)
	cancel()
	if err != nil {
		return err
	}
	a.ID, h.ID = s.ID, s.ID
	a.ExpiresAt, h.ExpiresAt = s.ExpiresAt, s.ExpiresAt
	a.RelayURL, h.RelayURL = *base, *base
	a.RelayToken, h.RelayToken = s.ClientToken, s.HostToken
	success := false
	defer func() {
		if !success {
			c, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if e := relay.Revoke(c, *base, s); e != nil {
				fmt.Fprintln(stderr, "Failed to revoke incomplete invitation:", e)
			}
		}
	}()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	config, err := access.WriteConfig(a, *dir, exe)
	if err != nil {
		return err
	}
	hostPath := filepath.Join(filepath.Dir(config), "host.ticket")
	if err := writeJSONPrivate(hostPath, h); err != nil {
		return err
	}
	// The host ticket is deliberately separate from the operator's private key.
	fmt.Fprintf(stdout, "Transfer host.ticket privately to the target, then run:\n  PowerShell: .\\shellpier.exe host '.\\host.ticket'\n  CMD:        shellpier.exe host host.ticket\n  Linux/mac:  ./shellpier host ./host.ticket\n\nHost ticket: %s\nOn this computer: ssh -F %s shellpier whoami\n", hostPath, displayArg(config))
	success = true
	return nil
}

func revoke(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: shellpier revoke HOST-TICKET")
	}
	t, err := access.Read(args[0])
	if err != nil {
		return err
	}
	if err := t.Validate("host"); err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := relay.Revoke(c, t.RelayURL, ticketSession(t)); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Session revoked; relay connections closed.")
	return err
}

func doctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("doctor", stderr)
	base := f.String("relay", os.Getenv("SHELLPIER_RELAY_URL"), "check the relay's HTTPS health endpoint")
	asJSON := f.Bool("json", false, "machine-readable diagnostics")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("doctor accepts flags only")
	}
	identity, identityErr := platform.Identity()
	if identityErr != nil {
		identity = "unavailable"
	}
	hostname, _ := os.Hostname()
	report := struct {
		Network  netutil.Report `json:"network"`
		User     string         `json:"user"`
		Hostname string         `json:"hostname"`
		Relay    string         `json:"relay_health,omitempty"`
		Notes    []string       `json:"notes"`
	}{Network: netutil.Inspect(), User: identity, Hostname: hostname, Notes: []string{
		"Direct access requires a successful peer-side SSH probe, not just a local IP address.",
		"For campus isolation, NAT, or phone hotspots, use a reachable HTTPS/WSS relay.",
		"Complete campus portal authentication normally; a phone hotspot or USB tether can provide another permitted network.",
		"HTTPS health does not prove WebSocket Upgrade works. A host + connect test is needed.",
		"No system routes, firewall rules, proxy settings, or SSH services were modified.",
	}}
	if *base != "" {
		report.Relay = probeHealth(ctx, *base)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintf(stdout, "System: %s/%s\nUser: %s\nHost: %s\n", report.Network.OS, report.Network.Arch, report.User, report.Hostname)
	for _, nic := range report.Network.Interfaces {
		if nic.Up {
			fmt.Fprintf(stdout, "Interface %s: %s\n", nic.Name, strings.Join(nic.Addresses, ", "))
		}
	}
	fmt.Fprintf(stdout, "Proxy variables set: %s\n", strings.Join(report.Network.ProxyVariables, ", "))
	if report.Relay != "" {
		fmt.Fprintln(stdout, "Relay:", report.Relay)
	}
	for _, note := range append(report.Notes, report.Network.Notes...) {
		fmt.Fprintln(stdout, "-", note)
	}
	return nil
}

func probeHealth(ctx context.Context, base string) string {
	u, err := url.Parse(base)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
		return "invalid relay URL; credentials/query/fragment are not allowed"
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return "HTTPS required except loopback HTTP"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/healthz"
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, u.String(), nil)
	if err != nil {
		return "invalid health request"
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		// Network errors can contain proxy credentials. Return categories only.
		var dns *net.DNSError
		if errors.As(err, &dns) {
			return "DNS lookup failed"
		}
		return "HTTPS connection failed; check DNS, proxy, certificate trust, network, and relay address"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Sprintf("HTTP %d; verify relay path and complete any captive-portal login", resp.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil || !result.OK {
		return "response is not a ShellPier relay health response; check portal, proxy, and URL"
	}
	return "HTTPS health OK; WebSocket and SSH authentication still need an end-to-end test"
}

func runRelay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("relay", stderr)
	listen := f.String("listen", "127.0.0.1:8080", "HTTP listener behind an HTTPS reverse proxy")
	keyFile := f.String("token-file", "", "relay enrollment key file")
	initKey := f.String("init-key", "", "create a new private relay key file and exit")
	maxSessions := f.Int("max-sessions", 64, "maximum simultaneous sessions")
	maxTTL := f.Duration("max-ttl", 24*time.Hour, "maximum nonpersistent session lifetime")
	persistent := f.Bool("allow-persistent", false, "explicitly permit sessions with TTL 0")
	cert := f.String("tls-cert", "", "PEM certificate for native HTTPS")
	key := f.String("tls-key", "", "PEM private key for native HTTPS")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("relay accepts flags only")
	}
	if *initKey != "" {
		value, err := secret()
		if err != nil {
			return err
		}
		if err := writePrivate(*initKey, []byte(value+"\n")); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Created relay key file:", *initKey)
		return nil
	}
	if (*cert == "") != (*key == "") || *maxTTL < time.Second {
		return errors.New("provide both TLS certificate and key; max-ttl must be at least 1s")
	}
	apiKey, err := tokenKey(*keyFile)
	if err != nil {
		return err
	}
	server, err := relay.New(apiKey, *maxSessions)
	if err != nil {
		return err
	}
	server.AllowPersistent, server.MaxTTL = *persistent, *maxTTL
	defer server.Close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan error, 1)
	go func() {
		if *cert != "" {
			done <- httpServer.ServeTLS(listener, *cert, *key)
		} else {
			done <- httpServer.Serve(listener)
		}
	}()
	fmt.Fprintln(stdout, "Relay listening:", listener.Addr())
	if *cert == "" {
		fmt.Fprintln(stdout, "Use an HTTPS reverse proxy before exposing this HTTP listener.")
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		server.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			httpServer.Close()
			return err
		}
		return nil
	}
}
