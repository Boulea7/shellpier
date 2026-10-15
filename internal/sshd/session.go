package sshd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	MaxEnvironmentKeys   = 32
	MaxEnvironmentBytes  = 16 * 1024
	MaxRequestBytes      = MaxCommandBytes + 4096
	MaxTerminalDimension = 1000
)

type capabilities struct {
	Protocol   int      `json:"protocol"`
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	Shell      string   `json:"shell"`
	PTY        bool     `json:"pty"`
	SFTP       bool     `json:"sftp"`
	Exec       bool     `json:"exec"`
	Env        bool     `json:"env"`
	CWD        bool     `json:"cwd"`
	ExecSignal bool     `json:"exec_signal"`
	PTYSignals []string `json:"pty_signals"`
	Forward    bool     `json:"forward"`
}

func (s *Server) globalRequests(requests <-chan *ssh.Request) {
	for request := range requests {
		if request.Type != "capabilities@shellpier" || len(request.Payload) != 0 {
			request.Reply(false, nil)
			continue
		}
		shell := s.Shell
		if shell == "" {
			shell = "auto"
		}
		result := capabilities{Protocol: 1, OS: runtime.GOOS, Arch: runtime.GOARCH, Shell: shell, PTY: platform.TerminalSupported(), SFTP: true, Exec: true, Env: true, CWD: true, PTYSignals: platform.TerminalSignals(), Forward: s.Forward != nil}
		payload, _ := json.Marshal(result)
		request.Reply(true, payload)
	}
}
func (s *Server) session(parent context.Context, ch ssh.Channel, requests <-chan *ssh.Request) {
	ctx, cancel := context.WithCancel(parent)
	var terminal platform.Terminal
	var subsystem *sftp.Server
	var finished chan struct{}
	defer func() {
		cancel()
		ch.Close()
		if terminal != nil {
			terminal.Close()
		}
		if subsystem != nil {
			subsystem.Close()
		}
		if finished != nil {
			<-finished
		}
	}()
	options := platform.TerminalOptions{Shell: s.Shell, WorkDir: s.WorkDir, Columns: 80, Rows: 24}
	environment := map[string]string{}
	ptyRequested, started := false, false
	run := func(work func()) {
		finished = make(chan struct{})
		go func() { defer close(finished); work() }()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-finished:
			return
		case request, ok := <-requests:
			if !ok {
				return
			}
			accepted := false
			if len(request.Payload) > MaxRequestBytes {
				request.Reply(false, nil)
				continue
			}
			switch request.Type {
			case "env":
				var value struct{ Name, Value string }
				if !started && ssh.Unmarshal(request.Payload, &value) == nil && validEnvironment(value.Name, value.Value) {
					previous, existed := environment[value.Name]
					environment[value.Name] = value.Value
					if len(environment) <= MaxEnvironmentKeys && environmentBytes(environment) <= MaxEnvironmentBytes {
						accepted = true
					} else {
						if existed {
							environment[value.Name] = previous
						} else {
							delete(environment, value.Name)
						}
					}
				}
			case "cwd@shellpier":
				var value struct{ Directory string }
				if !started && ssh.Unmarshal(request.Payload, &value) == nil && len(value.Directory) > 0 && len(value.Directory) <= 4096 && !strings.ContainsRune(value.Directory, 0) {
					directory := value.Directory
					if !filepath.IsAbs(directory) {
						if filepath.VolumeName(directory) != "" {
							request.Reply(false, nil)
							continue
						}
						directory = filepath.Join(s.WorkDir, directory)
					}
					if absolute, err := filepath.Abs(directory); err == nil {
						if info, err := os.Stat(absolute); err == nil && info.IsDir() {
							options.WorkDir = absolute
							accepted = true
						}
					}
				}
			case "pty-req":
				var value struct {
					Term                         string
					Columns, Rows, Width, Height uint32
					Modes                        string
				}
				if !started && !ptyRequested && platform.TerminalSupported() && ssh.Unmarshal(request.Payload, &value) == nil && validTerminal(value.Term, value.Columns, value.Rows) && value.Width <= 100000 && value.Height <= 100000 && validTerminalModes(value.Modes) {
					ptyRequested = true
					options.Term = value.Term
					options.Columns, options.Rows = terminalDimensions(value.Columns, value.Rows, options.Columns, options.Rows)
					options.Modes = terminalModes(value.Modes)
					accepted = true
				}
			case "window-change":
				var value struct{ Columns, Rows, Width, Height uint32 }
				if ptyRequested && ssh.Unmarshal(request.Payload, &value) == nil && validDimensions(value.Columns, value.Rows) && value.Width <= 100000 && value.Height <= 100000 {
					columns, rows := terminalDimensions(value.Columns, value.Rows, options.Columns, options.Rows)
					accepted = terminal == nil || columns == options.Columns && rows == options.Rows || terminal.Resize(columns, rows) == nil
					if accepted {
						options.Columns, options.Rows = columns, rows
					}
				}
			case "signal":
				var value struct{ Name string }
				if started && terminal != nil && ssh.Unmarshal(request.Payload, &value) == nil && len(value.Name) <= 16 {
					accepted = terminal.Signal(value.Name) == nil
				}
			case "shell", "exec":
				command := ""
				valid := len(request.Payload) == 0
				if request.Type == "exec" {
					var value struct{ Command string }
					valid = ssh.Unmarshal(request.Payload, &value) == nil && len(value.Command) > 0 && len(value.Command) <= MaxCommandBytes && !strings.ContainsRune(value.Command, 0)
					command = value.Command
				}
				if !started && valid && (request.Type != "shell" || ptyRequested) {
					options.Env = environmentList(environment)
					options.Command = command
					if ptyRequested {
						var err error
						terminal, err = platform.StartTerminal(ctx, options)
						if err == nil {
							started = true
							accepted = true
							run(func() { s.runTerminal(ctx, ch, terminal) })
						} else {
							request.Reply(false, nil)
							return
						}
					} else {
						started = true
						accepted = true
						copyServer := *s
						copyServer.WorkDir = options.WorkDir
						run(func() { copyServer.execute(ctx, ch, command, options.Env) })
					}
				}
			case "subsystem":
				var value struct{ Name string }
				if !started && !ptyRequested && ssh.Unmarshal(request.Payload, &value) == nil && value.Name == "sftp" {
					var err error
					subsystem, err = sftp.NewServer(ch, sftp.WithServerWorkingDirectory(options.WorkDir))
					if err == nil {
						started = true
						accepted = true
						run(func() {
							err := subsystem.Serve()
							status := uint32(0)
							if err != nil && err != io.EOF {
								status = 1
							}
							sendStatus(ch, status)
						})
					}
				}
			}
			request.Reply(accepted, nil)
		}
	}
}
func (s *Server) runTerminal(ctx context.Context, ch ssh.Channel, terminal platform.Terminal) {
	go io.Copy(terminal, ch)
	drained := make(chan struct{})
	go func() { defer close(drained); io.Copy(ch, terminal) }()
	status, err := terminal.Wait()
	if err != nil && status == 0 {
		status = 255
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-drained:
	case <-ctx.Done():
	case <-timer.C:
	}
	terminal.Close()
	// A peer that does not consume output must not pin a completed session.
	drainTimer := time.NewTimer(100 * time.Millisecond)
	defer drainTimer.Stop()
	select {
	case <-drained:
		sendStatus(ch, status)
	case <-ctx.Done():
		ch.Close()
		<-drained
	case <-drainTimer.C:
		sendStatus(ch, status)
		ch.Close()
		<-drained
	}

}
func validDimensions(columns, rows uint32) bool {
	return columns <= MaxTerminalDimension && rows <= MaxTerminalDimension
}
func validTerminal(term string, columns, rows uint32) bool {
	if len(term) == 0 || len(term) > 64 || !validDimensions(columns, rows) {
		return false
	}
	for _, char := range term {
		if char < '!' || char > '~' {
			return false
		}
	}
	return true
}
func validTerminalModes(modes string) bool {
	if len(modes) > 4096 {
		return false
	}
	for offset := 0; offset < len(modes); {
		opcode := modes[offset]
		offset++
		if opcode == 0 {
			return offset == len(modes)
		}
		if opcode >= 160 {
			return true
		}
		if len(modes)-offset < 4 {
			return false
		}
		offset += 4
	}
	return false
}
func validEnvironment(name, value string) bool {
	if len(name) == 0 || len(name) > 64 || len(value) > 4096 || strings.ContainsRune(value, 0) {
		return false
	}
	for i, char := range name {
		if char != '_' && !(char >= 'A' && char <= 'Z') && !(char >= 'a' && char <= 'z') && !(i > 0 && char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}
func environmentBytes(env map[string]string) int {
	total := 0
	for key, value := range env {
		total += len(key) + len(value) + 1
	}
	return total
}
func environmentList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}
	return values
}
func mergeEnvironment(base, overrides []string) []string {
	values := map[string]string{}
	names := map[string]string{}
	for _, entry := range append(append([]string(nil), base...), overrides...) {
		name, value, ok := strings.Cut(entry, "=")
		if name == "" && strings.HasPrefix(entry, "=") {
			suffix, remainder, found := strings.Cut(entry[1:], "=")
			name = "=" + suffix
			value = remainder
			ok = found
		}
		if !ok {
			continue
		}
		key := name
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		values[key] = value
		names[key] = name
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, names[key]+"="+value)
	}
	return out
}

func terminalModes(encoded string) map[uint8]uint32 {
	values := map[uint8]uint32{}
	for offset := 0; offset < len(encoded); {
		opcode := encoded[offset]
		offset++
		if opcode == 0 || opcode >= 160 {
			break
		}
		if len(encoded)-offset < 4 {
			break
		}
		values[opcode] = uint32(encoded[offset])<<24 | uint32(encoded[offset+1])<<16 | uint32(encoded[offset+2])<<8 | uint32(encoded[offset+3])
		offset += 4
	}
	return values
}

// RFC 4254 section 6.2 requires zero dimension parameters to be ignored.
func terminalDimensions(columns, rows, currentColumns, currentRows uint32) (uint32, uint32) {
	if columns == 0 {
		columns = currentColumns
	}
	if rows == 0 {
		rows = currentRows
	}
	return columns, rows
}
