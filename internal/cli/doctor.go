package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/netutil"
	"github.com/Boulea7/shellpier/internal/platform"
)

func doctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("doctor", stderr)
	base := f.String("relay", os.Getenv("SHELLPIER_RELAY_URL"), "check the relay's HTTPS health endpoint")
	asJSON := f.Bool("json", false, "machine-readable diagnostics")
	if err := parseFlags(f, args); err != nil {
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
