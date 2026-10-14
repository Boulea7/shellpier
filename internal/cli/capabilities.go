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
)

type remoteCapabilities struct {
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
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client, err := access.DialRetry(ctx, ticket, *iface, *retry)
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
	if len(data) > 4096 || json.Unmarshal(data, &result) != nil || result.Protocol != 1 || !capabilityName(result.OS) || !capabilityName(result.Arch) || !capabilityName(result.Shell) {
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
	if *asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	_, err = fmt.Fprintf(stdout, "Target: %s/%s\nShell: %s\nPTY: %t\nPTY signals: %s\nExec: %t\nSFTP: %t\nEnvironment: %t\nWorking directory: %t\n", result.OS, result.Arch, result.Shell, result.PTY, strings.Join(result.PTYSignals, ", "), result.Exec, result.SFTP, result.Env, result.CWD)
	return err
}

func capabilityName(value string) bool {
	return len(value) > 0 && len(value) <= 64 && strings.IndexFunc(value, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.')
	}) < 0
}
