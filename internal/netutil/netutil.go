// Package netutil provides interface-scoped networking and privacy-safe diagnostics.
package netutil

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Dial opens a direct TCP connection. An explicit interface constrains the
// socket's routing interface rather than merely selecting a source address.
func Dial(ctx context.Context, address, iface string) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	if iface != "" {
		nic, err := net.InterfaceByName(iface)
		if err != nil {
			return nil, fmt.Errorf("interface %q: %w", iface, err)
		}
		if nic.Flags&net.FlagUp == 0 {
			return nil, fmt.Errorf("interface %q is down", iface)
		}
		d.Control = func(network, address string, raw syscall.RawConn) error {
			var bindErr error
			if err := raw.Control(func(fd uintptr) { bindErr = bindInterface(fd, network, nic) }); err != nil {
				return err
			}
			return bindErr
		}
	}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil && iface != "" {
		return nil, fmt.Errorf("dial via interface %q: %w", iface, err)
	}
	return conn, err
}

// Addresses returns candidate listener endpoints. Candidates are not evidence
// of reachability from another device or across an access point.
func Addresses(listenAddr net.Addr, iface string) ([]string, error) {
	if listenAddr == nil {
		return nil, fmt.Errorf("nil listener address")
	}
	host, port, err := net.SplitHostPort(listenAddr.String())
	if err != nil {
		return nil, err
	}
	if iface != "" {
		if _, err := net.InterfaceByName(iface); err != nil {
			return nil, fmt.Errorf("interface %q: %w", iface, err)
		}
	}
	ip := net.ParseIP(strings.Split(host, "%")[0])
	if host != "" && (ip == nil || !ip.IsUnspecified()) {
		return []string{net.JoinHostPort(host, port)}, nil
	}
	nics, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, nic := range nics {
		if iface != "" && nic.Name != iface {
			continue
		}
		if nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := nic.Addrs()
		if err != nil {
			return nil, fmt.Errorf("interface %q addresses: %w", nic.Name, err)
		}
		candidates = append(candidates, interfaceEndpoints(addrs, port, ip != nil && ip.To4() != nil)...)
	}
	if len(candidates) == 0 && iface != "" {
		return nil, fmt.Errorf("interface %q has no eligible listener addresses", iface)
	}
	return sortEndpoints(candidates, port), nil
}
func interfaceEndpoints(addrs []net.Addr, port string, ipv4Only bool) []string {
	out := []string{}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || (ipv4Only && ip.To4() == nil) {
			continue
		}
		out = append(out, net.JoinHostPort(ip.String(), port))
	}
	return out
}
func sortEndpoints(candidates []string, port string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(candidates))
	for _, v := range candidates {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _, _ := net.SplitHostPort(out[i])
		b, _, _ := net.SplitHostPort(out[j])
		a4 := net.ParseIP(a).To4() != nil
		b4 := net.ParseIP(b).To4() != nil
		if a4 != b4 {
			return a4
		}
		return out[i] < out[j]
	})
	if len(out) == 0 {
		return []string{net.JoinHostPort("127.0.0.1", port)}
	}
	return out
}

type Interface struct {
	Name      string   `json:"name"`
	Index     int      `json:"index"`
	Up        bool     `json:"up"`
	Addresses []string `json:"addresses"`
	Error     string   `json:"error,omitempty"`
}
type Report struct {
	OS             string      `json:"os"`
	Arch           string      `json:"arch"`
	Interfaces     []Interface `json:"interfaces"`
	ProxyVariables []string    `json:"proxy_variables"`
	Notes          []string    `json:"notes"`
	Error          string      `json:"error,omitempty"`
}

// Inspect observes local state without changing routes or exposing proxy values.
func Inspect() Report {
	r := Report{OS: runtime.GOOS, Arch: runtime.GOARCH, Interfaces: []Interface{}, ProxyVariables: []string{}, Notes: []string{"Listener addresses are candidates; reachability requires a probe from the peer device.", "Captive portals and AP/client isolation can prevent local communication despite an active Wi-Fi connection.", "DNS resolution, IPv4 reachability, and IPv6 reachability must be checked separately.", "Proxy environment variables do not describe system proxy settings or VPN routing; absence does not prove that a VPN is inactive.", "An explicit interface constrains the TCP socket's route; DNS resolution may still follow system resolver and VPN settings."}}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			r.ProxyVariables = append(r.ProxyVariables, name)
		}
	}
	nics, err := net.Interfaces()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	for _, nic := range nics {
		item := Interface{Name: nic.Name, Index: nic.Index, Up: nic.Flags&net.FlagUp != 0, Addresses: []string{}}
		addrs, err := nic.Addrs()
		if err != nil {
			item.Error = err.Error()
		} else {
			for _, addr := range addrs {
				item.Addresses = append(item.Addresses, addr.String())
			}
		}
		r.Interfaces = append(r.Interfaces, item)
	}
	return r
}
