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

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/netutil"
	"github.com/Boulea7/shellpier/internal/platform"
)

func doctor(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("doctor", stderr)
	base := f.String("relay", os.Getenv("SHELLPIER_RELAY_URL"), "check the relay's HTTPS health endpoint")
	asJSON := f.Bool("json", false, "machine-readable diagnostics")
	target := f.String("target", "", "ticket or target:NAME for SSH and capability diagnostics")
	timeout := f.Duration("timeout", 20*time.Second, "total target diagnostic timeout")
	iface := f.String("interface", "", "direct target routing interface")
	retry := clientRetryFlags(f)
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("doctor accepts flags only")
	}
	if *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("doctor timeout must be positive and at most 5m")
	}
	if err := retry.Validate(); err != nil {
		return err
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
		Target   []doctorStage  `json:"target_stages,omitempty"`
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
	var targetErr error
	if *target != "" {
		targetCtx, cancel := context.WithTimeout(ctx, *timeout)
		report.Target, targetErr = diagnoseTarget(targetCtx, *target, *iface, *retry)
		cancel()
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		return targetErr
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
	for _, stage := range report.Target {
		fmt.Fprintf(stdout, "Target %s: %t (%s)\n", stage.Stage, stage.OK, stage.Code)
	}
	return targetErr
}

type doctorStage struct {
	Stage string `json:"stage"`
	OK    bool   `json:"ok"`
	Code  string `json:"code"`
}

func diagnoseTarget(ctx context.Context, input, iface string, retry access.RetryOptions) ([]doctorStage, error) {
	stages := []doctorStage{}
	fail := func(stage, code string) ([]doctorStage, error) {
		stages = append(stages, doctorStage{stage, false, code})
		return stages, fmt.Errorf("target diagnostic failed at %s: %s", stage, code)
	}
	ticket, err := access.Read(input)
	if err != nil {
		return fail("ticket", "unreadable_or_invalid")
	}
	if err = ticket.Validate("access"); err != nil {
		return fail("ticket", "invalid_or_expired")
	}
	stages = append(stages, doctorStage{"ticket", true, "valid"})
	client, err := access.DialRetry(ctx, ticket, iface, retry)
	if err != nil {
		if ctx.Err() != nil {
			return fail("ssh", "canceled_or_timeout")
		}
		return fail("ssh", "connection_or_authentication_failed")
	}
	defer client.Close()
	stages = append(stages, doctorStage{"ssh", true, "authenticated"})
	if _, err = readIdentity(client, "hostname"); err != nil {
		return fail("identity", "hostname_failed")
	}
	if _, err = readIdentity(client, "whoami"); err != nil {
		return fail("identity", "account_failed")
	}
	stages = append(stages, doctorStage{"identity", true, "verified"})
	ok, data, err := client.SendRequest("capabilities@shellpier", true, nil)
	if err != nil {
		return fail("capabilities", "request_failed")
	}
	if !ok {
		return fail("capabilities", "unsupported")
	}
	var result remoteCapabilities
	if len(data) > 4096 || json.Unmarshal(data, &result) != nil || result.Protocol != 1 || !capabilityName(result.OS) || !capabilityName(result.Arch) || !capabilityName(result.Shell) {
		return fail("capabilities", "invalid_response")
	}
	stages = append(stages, doctorStage{"capabilities", true, "advertised"})
	return stages, nil
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
