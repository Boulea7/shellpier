package netutil

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoopbackDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if c != nil {
			c.Close()
		}
		accepted <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := Dial(ctx, listener.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}
func TestInvalidInterface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, err := Dial(ctx, "127.0.0.1:1", "shellpier-no-such-interface"); err == nil {
		c.Close()
		t.Fatal("unknown interface accepted")
	}
	if _, err := Addresses(&net.TCPAddr{IP: net.IPv4zero, Port: 99}, "shellpier-no-such-interface"); err == nil {
		t.Fatal("unknown interface accepted for advertisement")
	}
}
func TestSpecifiedAddress(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "192.0.2.1", "2001:db8::1"} {
		addr := &net.TCPAddr{IP: net.ParseIP(ip), Port: 12345}
		endpoints, err := Addresses(addr, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(endpoints) != 1 || endpoints[0] != net.JoinHostPort(ip, "12345") {
			t.Fatalf("unexpected endpoints: %v", endpoints)
		}
	}
}
func TestInterfaceAddressFiltering(t *testing.T) {
	inputs := []net.Addr{}
	for _, value := range []string{"127.0.0.1", "169.254.3.4", "224.0.0.1", "0.0.0.0", "::1", "fe80::1", "ff02::1", "2001:db8::2", "192.0.2.8"} {
		inputs = append(inputs, &net.IPAddr{IP: net.ParseIP(value)})
	}
	got := sortEndpoints(interfaceEndpoints(inputs, "2222", false), "2222")
	want := []string{"192.0.2.8:2222", "[2001:db8::2]:2222"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected filtering: %v", got)
	}
	v4 := interfaceEndpoints(inputs, "2222", true)
	if len(v4) != 1 || v4[0] != want[0] {
		t.Fatalf("IPv4 listener advertises incompatible address: %v", v4)
	}
	fallback := sortEndpoints(nil, "2222")
	if len(fallback) != 1 || fallback[0] != "127.0.0.1:2222" {
		t.Fatal(fallback)
	}
}
func TestInspectProxyPrivacy(t *testing.T) {
	secret := "http://private-user:private-password@private-proxy.example:8080"
	t.Setenv("HTTP_PROXY", secret)
	r := Inspect()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "private-password") || strings.Contains(string(data), "private-proxy") {
		t.Fatal("proxy credentials leaked")
	}
	found := false
	for _, name := range r.ProxyVariables {
		if name == "HTTP_PROXY" {
			found = true
		}
	}
	if !found {
		t.Fatal("proxy environment variable omitted")
	}
	if r.OS == "" || r.Arch == "" {
		t.Fatal("platform missing")
	}
}

func TestDarwinLoopbackInterfaceDial(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin IP_BOUND_IF runtime verification")
	}
	nics, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, nic := range nics {
		if nic.Flags&net.FlagLoopback != 0 && nic.Flags&net.FlagUp != 0 {
			name = nic.Name
			break
		}
	}
	if name == "" {
		t.Skip("no active loopback interface")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if c != nil {
			c.Close()
		}
		accepted <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := Dial(ctx, listener.Addr().String(), name)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}
