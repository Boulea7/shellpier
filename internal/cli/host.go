package cli

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/netutil"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/relay"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

type hostOptions struct {
	awake        bool
	shell        string
	workdir      string
	output       string
	json         bool
	reconnectMin time.Duration
	reconnectMax time.Duration
}

func share(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("share", stderr)
	base := f.String("relay", os.Getenv("SHELLPIER_RELAY_URL"), "HTTPS relay URL; unset uses direct mode")
	keyFile := f.String("token-file", "", "relay enrollment key file")
	ttl := f.Duration("ttl", defaultSessionTTL, "authorization lifetime; 0 means explicitly persistent")
	listen := f.String("listen", "0.0.0.0:0", "direct SSH listen address; ignored in relay mode")
	iface := f.String("interface", "", "advertise addresses on this interface; --listen controls binding")
	o := hostOptions{}
	f.DurationVar(&o.reconnectMin, "reconnect-min", defaultReconnectMin, "minimum relay reconnect delay")
	f.DurationVar(&o.reconnectMax, "reconnect-max", defaultReconnectMax, "maximum relay reconnect delay")
	f.BoolVar(&o.awake, "keep-awake", false, "temporarily inhibit automatic system sleep")
	f.StringVar(&o.shell, "shell", "auto", "auto, powershell, cmd, or sh")
	f.StringVar(&o.workdir, "work-dir", "", "remote command starting directory")
	f.StringVar(&o.output, "output", "", "write an access ticket to this new private file")
	f.BoolVar(&o.json, "json", false, "emit session metadata as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *ttl < 0 || *ttl > 0 && *ttl < time.Second {
		return errors.New("share accepts flags only; ttl must be 0 or at least 1s")
	}
	if *base != "" && *iface != "" {
		return errors.New("--interface applies to direct mode; configure HTTPS_PROXY for a relay")
	}
	if err := validateHostOptions(o); err != nil {
		return err
	}
	a, h, err := access.NewPair(*ttl)
	if err != nil {
		return err
	}
	var listener net.Listener
	if *base != "" {
		key, err := tokenKey(*keyFile)
		if err != nil {
			return err
		}
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		s, err := relay.Create(c, *base, key, *ttl)
		cancel()
		if err != nil {
			return err
		}
		a.ID, h.ID = s.ID, s.ID
		a.ExpiresAt, h.ExpiresAt = s.ExpiresAt, s.ExpiresAt
		a.RelayURL, h.RelayURL = *base, *base
		a.RelayToken, h.RelayToken = s.ClientToken, s.HostToken
	} else {
		listener, err = net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		defer listener.Close()
		a.Addresses, err = netutil.Addresses(listener.Addr(), *iface)
		if err != nil {
			return err
		}
	}
	return serveHost(ctx, h, &a, listener, o, stdout, stderr)
}

func host(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	input, args := ticketArgument(args)
	f := flags("host", stderr)
	o := hostOptions{}
	f.DurationVar(&o.reconnectMin, "reconnect-min", defaultReconnectMin, "minimum relay reconnect delay")
	f.DurationVar(&o.reconnectMax, "reconnect-max", defaultReconnectMax, "maximum relay reconnect delay")
	f.BoolVar(&o.awake, "keep-awake", false, "temporarily inhibit automatic system sleep")
	f.StringVar(&o.shell, "shell", "auto", "auto, powershell, cmd, or sh")
	f.StringVar(&o.workdir, "work-dir", "", "remote command starting directory")
	f.BoolVar(&o.json, "json", false, "emit session metadata as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if input == "" && f.NArg() == 1 {
		input = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("host accepts one ticket")
	}
	if input == "" {
		return errors.New("usage: shellpier host HOST-TICKET [--keep-awake]")
	}
	if err := validateHostOptions(o); err != nil {
		return err
	}
	t, err := access.Read(input)
	if err != nil {
		return err
	}
	if err := t.Validate("host"); err != nil {
		return err
	}
	return serveHost(ctx, t, nil, nil, o, stdout, stderr)
}

func validateHostOptions(o hostOptions) error {
	if o.reconnectMin <= 0 || o.reconnectMax < o.reconnectMin || o.reconnectMax > 24*time.Hour {
		return errors.New("reconnect-min must be positive; reconnect-max must be at least reconnect-min and at most 24h")
	}
	if _, err := platform.Command(o.shell, "", o.workdir); err != nil {
		return err
	}
	if o.workdir != "" {
		info, err := os.Stat(o.workdir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("work-dir must be a directory")
		}
	}
	return nil
}

type localState struct {
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	ControlURL string    `json:"control_url"`
	Secret     string    `json:"secret"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func serveHost(parent context.Context, h access.Ticket, a *access.Ticket, listener net.Listener, o hostOptions, stdout, stderr io.Writer) (result error) {
	ctx, cancel := context.WithCancel(parent)
	if !h.ExpiresAt.IsZero() {
		cancel()
		ctx, cancel = context.WithDeadline(parent, h.ExpiresAt)
	}
	defer cancel()
	if h.RelayURL != "" {
		defer func() {
			c, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if err := relay.Revoke(c, h.RelayURL, ticketSession(h)); err != nil {
				fmt.Fprintln(stderr, "Local session closed; relay revocation could not be confirmed. Retry shellpier revoke using the host ticket.")
			}
		}()
	}
	if err := h.Validate("host"); err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if err != nil {
		return errors.New("invalid host private key")
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
	if err != nil {
		return errors.New("invalid authorized public key")
	}
	server := &sshd.Server{Signer: signer, AuthorizedKey: public, Shell: o.shell, WorkDir: o.workdir}
	dir, err := os.MkdirTemp("", "shellpier-")
	if err != nil {
		return err
	}
	statePath := filepath.Join(dir, "state.json")
	defer func() {
		if e := os.Remove(statePath); e != nil && !errors.Is(e, os.ErrNotExist) {
			result = errors.Join(result, e)
		}
		if e := os.Remove(dir); e != nil && !errors.Is(e, os.ErrNotExist) {
			result = errors.Join(result, e)
		}
	}()
	if err := platform.SecureDir(dir); err != nil {
		return err
	}
	if o.awake {
		release, err := platform.KeepAwake(ctx)
		if err != nil {
			return fmt.Errorf("keep-awake could not be enabled: %w", err)
		}
		defer func() { result = errors.Join(result, release()) }()
	}
	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	controlSecret, err := secret()
	if err != nil {
		controlListener.Close()
		return err
	}
	state := localState{Version: 1, Kind: "shellpier-host", ID: h.ID, ControlURL: "http://" + controlListener.Addr().String(), Secret: controlSecret, ExpiresAt: h.ExpiresAt}
	var active atomic.Int64
	controlServer := &http.Server{ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+state.Secret)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": h.ID, "expires_at": h.ExpiresAt, "active_ssh_connections": active.Load(), "status": "running"})
			return
		}
		if r.URL.Path == "/stop" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, "stopping\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			cancel()
			return
		}
		http.NotFound(w, r)
	})}
	defer controlServer.Close()
	go func() {
		if e := controlServer.Serve(controlListener); e != nil && !errors.Is(e, http.ErrServerClosed) {
			cancel()
		}
	}()
	if err := writeJSONPrivate(statePath, state); err != nil {
		return err
	}
	var encoded string
	if a != nil {
		if o.output != "" {
			if err := writeJSONPrivate(o.output, a); err != nil {
				return err
			}
		} else {
			encoded, err = a.Encode()
			if err != nil {
				return err
			}
		}
	}
	identity, _ := platform.Identity()
	metadata := struct {
		Event     string    `json:"event"`
		ID        string    `json:"id"`
		User      string    `json:"user"`
		State     string    `json:"state"`
		ExpiresAt time.Time `json:"expires_at"`
		Addresses []string  `json:"addresses,omitempty"`
		Ticket    string    `json:"ticket,omitempty"`
		Output    string    `json:"output,omitempty"`
	}{Event: "session_created", ID: h.ID, User: identity, State: statePath, ExpiresAt: h.ExpiresAt, Ticket: encoded, Output: o.output}
	if a != nil {
		metadata.Addresses = a.Addresses
	}
	if o.json {
		if err := json.NewEncoder(stdout).Encode(metadata); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "Session: %s\nCommands run as: %s\nState: %s\n", h.ID, identity, statePath)
		if h.ExpiresAt.IsZero() {
			fmt.Fprintln(stdout, "Lifetime: persistent until stopped")
		} else {
			fmt.Fprintln(stdout, "Expires:", h.ExpiresAt.Local().Format(time.RFC3339))
		}
		if a != nil {
			if o.output != "" {
				fmt.Fprintln(stdout, "Access ticket:", o.output)
			} else {
				fmt.Fprintf(stdout, "On your agent computer, run:\n  shellpier connect %s -- whoami\n", encoded)
			}
		}
		fmt.Fprintln(stdout, "Keep this window open. Ctrl+C or shellpier stop revokes this session.")
		if h.RelayURL != "" {
			fmt.Fprintln(stdout, "Waiting for outbound relay pairing; verify with a real SSH command.")
		}
	}
	var workers sync.WaitGroup
	var serveErr error
	if listener != nil {
		defer listener.Close()
		go func() { <-ctx.Done(); listener.Close() }()
		slots := make(chan struct{}, 16)
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() == nil {
					serveErr = err
					cancel()
				}
				break
			}
			select {
			case slots <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-slots }()
				active.Add(1)
				defer active.Add(-1)
				server.ServeConn(ctx, conn)
			}()
		}
	} else {
		var lastNotice atomic.Int64
		var failureOnce sync.Once
		for i := 0; i < 4; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				err := reconnectHost(ctx, o.reconnectMin, o.reconnectMax,
					func(ctx context.Context) (net.Conn, error) { return relay.DialHost(ctx, h.RelayURL, ticketSession(h)) },
					func(ctx context.Context, conn net.Conn) {
						active.Add(1)
						defer active.Add(-1)
						server.ServeConn(ctx, conn)
					},
					func() {
						now := time.Now().Unix()
						previous := lastNotice.Load()
						if now-previous >= 30 && lastNotice.CompareAndSwap(previous, now) {
							fmt.Fprintln(stderr, "Relay unavailable; reconnecting within this session's lifetime. Interrupted commands are not replayed.")
						}
					},
				)
				if permanentRelayError(err) {
					failureOnce.Do(func() {
						if !errors.Is(err, relay.ErrSessionClosed) {
							serveErr = err
						}
						cancel()
					})
				}
			}()
		}
		<-ctx.Done()
	}
	cancel()
	workers.Wait()
	if o.json {
		json.NewEncoder(stdout).Encode(map[string]string{"event": "session_closed", "id": h.ID})
	} else {
		fmt.Fprintln(stdout, "Session closed. Access revoked; managed SSH commands stopped.")
	}
	return serveErr
}

func control(ctx context.Context, mode string, args []string, stdout, stderr io.Writer) error {
	f := flags(mode, stderr)
	path := f.String("state", "", "state.json path printed by share or host")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *path == "" || f.NArg() != 0 {
		return errors.New("provide --state FILE")
	}
	b, err := readRegular(*path, 8192)
	if err != nil {
		return err
	}
	var state localState
	if json.Unmarshal(b, &state) != nil || state.Version != 1 || state.Kind != "shellpier-host" || len(state.Secret) != 64 {
		return errors.New("invalid ShellPier state file")
	}
	u, err := url.Parse(state.ControlURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || !net.ParseIP(u.Hostname()).IsLoopback() {
		return errors.New("invalid local control address")
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	method := http.MethodGet
	if mode == "stop" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(c, method, state.ControlURL+"/"+mode, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.Secret)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("local session is not reachable; it may already have stopped")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("local control returned HTTP %d", resp.StatusCode)
	}
	if mode == "status" {
		_, err = io.Copy(stdout, io.LimitReader(resp.Body, 8192))
		return err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Lstat(*path); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "Session stopped; local runtime files removed.")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("stop accepted; cleanup is still in progress, check the target window")
		case <-tick.C:
		}
	}
}

func cleanup(args []string, stdout, stderr io.Writer) error {
	f := flags("cleanup", stderr)
	dir := f.String("dir", "", "directory created by ssh-config or invite")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *dir == "" || f.NArg() != 0 {
		return errors.New("provide --dir GENERATED-DIRECTORY")
	}
	info, err := os.Lstat(*dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cleanup target must be a real directory")
	}
	b, err := readRegular(filepath.Join(*dir, "shellpier-manifest.json"), 8192)
	if err != nil {
		return err
	}
	var manifest struct {
		Version int      `json:"version"`
		ID      string   `json:"id"`
		Files   []string `json:"files"`
	}
	if json.Unmarshal(b, &manifest) != nil || manifest.Version != 1 || manifest.ID == "" || len(manifest.Files) != 4 {
		return errors.New("invalid generated-file manifest")
	}
	allowed := map[string]bool{"access.json": true, "identity": true, "known_hosts": true, "ssh_config": true}
	seen := map[string]bool{}
	for _, name := range manifest.Files {
		if !allowed[name] || seen[name] {
			return errors.New("invalid owned-file entry")
		}
		seen[name] = true
	}
	// An invite adds a host ticket only when it belongs to the same session.
	hostPath := filepath.Join(*dir, "host.ticket")
	if info, err := os.Lstat(hostPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("unexpected host.ticket file type; preserved")
		}
		hb, err := readRegular(hostPath, 32*1024)
		if err != nil {
			return err
		}
		var ticket struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(hb, &ticket) != nil || ticket.ID != manifest.ID || ticket.Kind != "host" {
			return errors.New("host.ticket does not belong to this generated session; preserved")
		}
		manifest.Files = append(manifest.Files, "host.ticket")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range append(manifest.Files, "shellpier-manifest.json") {
		path := filepath.Join(*dir, name)
		if entry, err := os.Lstat(path); err == nil && entry.IsDir() {
			return errors.New("generated-file path is now a directory; preserved")
		}
	}
	for _, name := range append(manifest.Files, "shellpier-manifest.json") {
		if err := os.Remove(filepath.Join(*dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	remaining, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		if err := os.Remove(*dir); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(stdout, "Unrelated files preserved in:", *dir)
	}
	fmt.Fprintln(stdout, "Generated client credentials removed. To revoke a running target, use stop or revoke before cleanup.")
	return nil
}
