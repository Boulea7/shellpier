package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var localSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// readLocalState accepts only the local control endpoint created by serveHost.
func readLocalState(path string) (localState, error) {
	var state localState
	if err := checkPrivateSessionFile(path); err != nil {
		return state, errors.New("session state is not a private owned file")
	}
	b, err := readRegular(path, 8192)
	if err != nil {
		return state, errors.New("cannot read session state")
	}
	if json.Unmarshal(b, &state) != nil || state.Version != 1 || state.Kind != "shellpier-host" || !localSessionID.MatchString(state.ID) || len(state.Secret) != 64 {
		return localState{}, errors.New("invalid ShellPier state file")
	}
	if _, err := hex.DecodeString(state.Secret); err != nil {
		return localState{}, errors.New("invalid ShellPier state file")
	}
	u, err := url.Parse(state.ControlURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || u.RawPath != "" {
		return localState{}, errors.New("invalid local control address")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || u.Host != net.JoinHostPort("127.0.0.1", u.Port()) {
		return localState{}, errors.New("invalid local control address")
	}
	if state.Transport != "" && state.Transport != "direct" && state.Transport != "relay" {
		return localState{}, errors.New("invalid session transport")
	}
	return state, nil
}

type sessionEntry struct {
	ID        string    `json:"id,omitempty"`
	StatePath string    `json:"state_path"`
	ExpiresAt time.Time `json:"expires_at"`
	Transport string    `json:"transport"`
	Activity  int64     `json:"activity"`
	Alive     bool      `json:"alive"`
	Stale     bool      `json:"stale"`
	Error     string    `json:"error,omitempty"`
}

func sessions(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("sessions", stderr)
	machine := f.Bool("json", false, "emit session metadata as JSON")
	dir := f.String("dir", os.TempDir(), "directory containing private shellpier session directories")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("sessions accepts flags only")
	}
	directory, err := os.Open(*dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	truncated := false
	scanned := 0
	result := make([]sessionEntry, 0)
	states := make([]localState, 0)
scan:
	for {
		entries, err := directory.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			scanned++
			if scanned > 100000 {
				truncated = true
				break scan
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !strings.HasPrefix(entry.Name(), "shellpier-") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			sessionDir := filepath.Join(*dir, entry.Name())
			if checkPrivateSessionDir(sessionDir) != nil {
				continue
			}
			path := filepath.Join(sessionDir, "state.json")
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if len(result) >= 1000 {
				truncated = true
				break scan
			}
			item := sessionEntry{StatePath: path, Transport: "unknown", Stale: true}
			state, err := readLocalState(path)
			if err != nil {
				item.Error = err.Error()
			} else {
				item.ID = state.ID
				item.ExpiresAt = state.ExpiresAt
				if state.Transport != "" {
					item.Transport = state.Transport
				}
			}
			result = append(result, item)
			states = append(states, state)
		}
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if result[index].Error != "" {
					continue
				}
				probeSession(ctx, states[index], &result[index])
			}
		}()
	}
	for i := range result {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if *machine {
		return json.NewEncoder(stdout).Encode(map[string]any{"sessions": result, "truncated": truncated})
	}
	if truncated {
		fmt.Fprintln(stderr, "Session discovery reached its scan limit; results are incomplete.")
	}
	if _, err := fmt.Fprintln(stdout, "ID\tSTATE\tTRANSPORT\tACTIVE\tEXPIRES\tSTATE FILE"); err != nil {
		return err
	}
	for _, item := range result {
		status := "stale"
		if item.Alive {
			status = "running"
		}
		if _, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\t%s\t%s\n", item.ID, status, item.Transport, item.Activity, item.ExpiresAt.Format(time.RFC3339), item.StatePath); err != nil {
			return err
		}
	}
	return nil
}

func probeSession(parent context.Context, state localState, item *sessionEntry) {
	ctx, cancel := context.WithTimeout(parent, 750*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, state.ControlURL+"/status", nil)
	if err != nil {
		item.Error = "invalid local control address"
		return
	}
	req.Header.Set("Authorization", "Bearer "+state.Secret)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		item.Error = "local session is not reachable"
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		item.Error = "local session rejected status request"
		return
	}
	var status struct {
		ID        string `json:"id"`
		Active    int64  `json:"active_ssh_connections"`
		Transport string `json:"transport"`
		Status    string `json:"status"`
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(b) > 8192 || json.Unmarshal(b, &status) != nil || status.ID != state.ID || status.Status != "running" || status.Active < 0 {
		item.Error = "invalid local session response"
		return
	}
	if status.Transport == "direct" || status.Transport == "relay" {
		item.Transport = status.Transport
	}
	item.Activity = status.Active
	item.Alive = true
	item.Stale = false
}
