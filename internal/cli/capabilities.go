package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"golang.org/x/crypto/ssh"
)

type remoteCapabilities struct {
	HostTerminalsProtocol int      `json:"host_terminals_protocol,omitempty"`
	AccessMode            string   `json:"access_mode"`
	SFTPReadOnly          bool     `json:"sftp_read_only"`
	FileRoot              bool     `json:"file_root"`
	ReverseForward        bool     `json:"reverse_forward"`
	Protocol              int      `json:"protocol"`
	OS                    string   `json:"os"`
	Arch                  string   `json:"arch"`
	Shell                 string   `json:"shell"`
	PTY                   bool     `json:"pty"`
	SFTP                  bool     `json:"sftp"`
	Exec                  bool     `json:"exec"`
	Env                   bool     `json:"env"`
	CWD                   bool     `json:"cwd"`
	ExecSignal            bool     `json:"exec_signal"`
	PTYSignals            []string `json:"pty_signals"`
	Jobs                  bool     `json:"jobs"`
	JobsProtocol          int      `json:"jobs_protocol,omitempty"`
	Forward               bool     `json:"forward"`
}

func capabilities(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("capabilities", stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 20*time.Second, "total capability query timeout")
	asJSON := f.Bool("json", false, "emit capabilities advertised by the authenticated target")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 1 || *timeout <= 0 || *timeout > 5*time.Minute {
		return errors.New("usage: shellpier capabilities TICKET [--json]; timeout must be positive and at most 5m")
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	ctx = retry.Context(ctx)
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	ok, data, err := client.SendRequest("capabilities@shellpier", true, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if !ok {
		return errors.New("target does not support capability discovery; older targets still support exec and SFTP")
	}
	var result remoteCapabilities
	if len(data) > 4096 || json.Unmarshal(data, &result) != nil {
		return errors.New("target returned invalid capabilities")
	}
	if err := validateRemoteCapabilities(&result); err != nil {
		return err
	}

	if *asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	_, err = fmt.Fprintf(stdout, "Target: %s/%s\nAccess mode: %s\nSFTP read only: %t\nDirectory root: %t\nShell: %s\nPTY: %t\nPTY signals: %s\nHost terminal protocol: %d\nExec: %t\nSFTP: %t\nEnvironment: %t\nWorking directory: %t\nJobs: %t\nPort forwarding: %t\nReverse forwarding: %t\n", result.OS, result.Arch, result.AccessMode, result.SFTPReadOnly, result.FileRoot, result.Shell, result.PTY, strings.Join(result.PTYSignals, ", "), result.HostTerminalsProtocol, result.Exec, result.SFTP, result.Env, result.CWD, result.Jobs, result.Forward, result.ReverseForward)
	return err
}

func capabilityName(value string) bool {
	return len(value) > 0 && len(value) <= 64 && strings.IndexFunc(value, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.')
	}) < 0
}

func validateRemoteCapabilities(result *remoteCapabilities) error {
	if result.Protocol != 1 || !capabilityName(result.OS) || !capabilityName(result.Arch) || !capabilityName(result.Shell) {
		return errors.New("target returned invalid capabilities")
	}
	if len(result.PTYSignals) > 32 || !result.PTY && len(result.PTYSignals) != 0 {
		return errors.New("target returned invalid terminal signal capabilities")
	}
	for _, signal := range result.PTYSignals {
		if !capabilityName(signal) {
			return errors.New("target returned invalid terminal signal capabilities")
		}
	}
	if result.Jobs && result.JobsProtocol != 1 || !result.Jobs && result.JobsProtocol != 0 {
		return errors.New("target returned invalid job capabilities")
	}
	if result.HostTerminalsProtocol < 0 || result.HostTerminalsProtocol > 1 || result.HostTerminalsProtocol != 0 && !result.PTY {
		return errors.New("target returned invalid host terminal capabilities")
	}
	if result.AccessMode == "" {
		result.AccessMode = "full"
	}
	switch result.AccessMode {
	case "full":
		if result.SFTPReadOnly || result.FileRoot {
			return errors.New("target returned contradictory access capabilities")
		}
	case "sftp", "sftp-readonly":
		if !result.SFTP || result.Exec || result.PTY || result.Env || result.CWD || result.ExecSignal || result.Jobs || result.Forward || result.ReverseForward || result.HostTerminalsProtocol != 0 || len(result.PTYSignals) != 0 || result.SFTPReadOnly != (result.AccessMode == "sftp-readonly") {
			return errors.New("target returned contradictory restricted capabilities")
		}
	default:
		return errors.New("target returned invalid access mode")
	}
	return nil
}

// queryRemoteCapabilities preserves discovery compatibility with older SSH targets.
func queryRemoteCapabilities(client *ssh.Client) (remoteCapabilities, bool, error) {
	var result remoteCapabilities
	ok, data, err := client.SendRequest("capabilities@shellpier", true, nil)
	if err != nil {
		return result, false, err
	}
	if !ok {
		result.AccessMode = "full"
		return result, false, nil
	}
	if len(data) > 4096 || json.Unmarshal(data, &result) != nil {
		return result, true, errors.New("target returned invalid capabilities")
	}
	if err := validateRemoteCapabilities(&result); err != nil {
		return result, true, err
	}
	return result, true, nil
}
