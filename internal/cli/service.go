package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/sshd"
)

type serviceManifest struct {
	Version      int      `json:"version"`
	Name         string   `json:"name"`
	OS           string   `json:"os"`
	Program      string   `json:"program"`
	TicketFile   string   `json:"ticket_file"`
	Shell        string   `json:"shell"`
	WorkDir      string   `json:"work_dir,omitempty"`
	KeepAwake    bool     `json:"keep_awake"`
	Autostart    bool     `json:"autostart"`
	Marker       string   `json:"marker"`
	Resource     string   `json:"resource"`
	Registration string   `json:"registration"`
	AllowJobs    bool     `json:"allow_jobs"`
	AllowForward []string `json:"allow_forward,omitempty"`
	TicketID     string   `json:"ticket_id"`
}
type serviceEnvironment struct {
	os, home, exe, uid string
	run                func(context.Context, string, ...string) ([]byte, error)
	stopHost           func(context.Context, string) (bool, error)
}

func defaultServiceEnvironment() (serviceEnvironment, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return serviceEnvironment{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return serviceEnvironment{}, err
	}
	uid, err := serviceUserID()
	if err != nil {
		return serviceEnvironment{}, err
	}
	return serviceEnvironment{os: runtime.GOOS, home: home, exe: exe, uid: uid, stopHost: serviceStopLocal, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		operation, done := context.WithTimeout(ctx, 15*time.Second)
		defer done()
		cmd := exec.CommandContext(operation, name, args...)
		return cmd.CombinedOutput()
	}}, nil
}
func service(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier service plan|install NAME --ticket FILE|target:NAME [--autostart] [--dry-run] [--json]\n       shellpier service start|status|stop|uninstall NAME [--dry-run] [--json]\nUser-level only. install does not start the host; --autostart opts into login startup. Existing host tickets are neither created nor renewed.")
		return err
	}
	// Share flag registration with execution without resolving identity or touching services.
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" {
			return serviceWith(ctx, args, stdout, stderr, serviceEnvironment{})
		}
	}
	env, err := defaultServiceEnvironment()
	if err != nil {
		return err
	}
	return serviceWith(ctx, args, stdout, stderr, env)
}
func serviceWith(ctx context.Context, args []string, stdout, stderr io.Writer, env serviceEnvironment) error {
	if len(args) == 0 {
		return errors.New("provide service action")
	}
	action := args[0]
	switch action {
	case "plan", "install", "start", "status", "stop", "uninstall":
	default:
		return errors.New("unknown service action")
	}
	f := flags("service "+action, stderr)
	configDir := f.String("config-dir", "", "private user configuration directory")
	machine := f.Bool("json", false, "emit reviewable service metadata")
	dry := f.Bool("dry-run", false, "describe without changing files or service state")
	var ticketValue, workdirValue string
	shellValue := "auto"
	var autostartValue, awakeValue, allowJobsValue bool
	ticket, workdir, shell := &ticketValue, &workdirValue, &shellValue
	autostart, awake, allowJobs := &autostartValue, &awakeValue, &allowJobsValue
	var forwards serviceStrings
	if action == "plan" || action == "install" {
		f.StringVar(ticket, "ticket", "", "existing private host ticket file or target:NAME")
		f.BoolVar(autostart, "autostart", false, "start at future user logins; install does not start now")
		f.StringVar(shell, "shell", "auto", "auto, sh, cmd or powershell")
		f.StringVar(workdir, "work-dir", "", "host command starting directory")
		f.BoolVar(awake, "keep-awake", false, "request sleep inhibition while the host runs")
		f.BoolVar(allowJobs, "allow-jobs", false, "allow host-managed jobs")
		f.Var(&forwards, "allow-forward", "allowed host:port forwarding destination; repeatable")
	}
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	if f.NArg() != 1 || !serviceName(f.Arg(0)) {
		return errors.New("provide a service name using 1..48 ASCII letters, digits, hyphens or underscores")
	}
	if env.os != "darwin" && env.os != "linux" && env.os != "windows" {
		return errors.New("user service manager is unavailable on this platform")
	}
	base, err := access.TargetConfigDir(*configDir)
	if err != nil {
		return err
	}
	dir := filepath.Join(base, "services", f.Arg(0))
	manifestPath := filepath.Join(dir, "manifest.json")
	var manifest serviceManifest
	if action == "plan" || action == "install" {
		switch *shell {
		case "auto", "sh", "cmd", "powershell":
		default:
			return errors.New("unsupported service shell")
		}
		if len(forwards) > 32 {
			return errors.New("too many forwarding destinations")
		}
		for i, value := range forwards {
			normalized, e := sshd.NormalizeForwardTarget(value)
			if e != nil {
				return e
			}
			forwards[i] = normalized
		}
		ref := *ticket
		if strings.HasPrefix(ref, "target:") {
			ref, err = access.ResolveTarget(*configDir, strings.TrimPrefix(ref, "target:"))
			if err != nil {
				return err
			}
		} else {
			ref = strings.TrimPrefix(ref, "@")
		}
		if ref == "" {
			return errors.New("provide --ticket with an existing host ticket")
		}
		ref, err = filepath.Abs(ref)
		if err != nil {
			return err
		}
		if err = serviceHostTicket(ref); err != nil {
			return err
		}
		for _, value := range []string{ref, env.exe, *workdir, env.home, env.uid} {
			if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
				return errors.New("invalid service path or identity")
			}
		}
		if !filepath.IsAbs(env.exe) {
			return errors.New("service executable must be absolute")
		}
		marker := make([]byte, 16)
		if _, err = rand.Read(marker); err != nil {
			return err
		}
		manifest = serviceManifest{Version: 1, Name: f.Arg(0), OS: env.os, Program: env.exe, TicketFile: ref, Shell: *shell, WorkDir: *workdir, KeepAwake: *awake, Autostart: *autostart, Marker: hex.EncodeToString(marker)}
		hostTicket, e := access.Read("@" + ref)
		if e != nil {
			return e
		}
		manifest.TicketID = hostTicket.ID
		manifest.AllowJobs = *allowJobs
		manifest.AllowForward = forwards
		manifest.Resource = serviceResource(env, manifest.Name, manifest.Autostart, dir)
		manifest.Registration = filepath.Join(dir, "registration"+serviceExtension(env.os))
	} else {
		manifest, err = readServiceManifest(manifestPath, env, f.Arg(0))
		if err != nil {
			return err
		}
		if action == "start" {
			if err = serviceHostTicket(manifest.TicketFile); err != nil {
				return err
			}
			current, e := access.Read("@" + manifest.TicketFile)
			if e != nil {
				return e
			}
			if current.ID != manifest.TicketID {
				return errors.New("host ticket identity changed; uninstall and reinstall the service explicitly")
			}
		}
	}
	definition := renderService(manifest, env, false)
	registration := renderService(manifest, env, true)
	if *dry && action != "plan" && action != "install" {
		if err = verifyServiceOwnership(manifest, env); err != nil {
			return err
		}
	}
	if *dry && action == "install" {
		if _, e := os.Lstat(manifest.Resource); !os.IsNotExist(e) {
			return errors.New("service resource already exists or cannot be inspected")
		}
	}
	if action == "plan" || *dry {
		return printServicePlan(stdout, *machine, action, manifest, definition, *dry)
	}
	graceful := false
	if action == "install" {
		// Check manager and name before writing anything.
		if err = serviceManagerAvailable(ctx, env); err != nil {
			return err
		}
		if _, err = os.Lstat(manifest.Resource); !os.IsNotExist(err) {
			return errors.New("service resource already exists or cannot be inspected")
		}
		registered, inspectionErr := verifyServiceManagerOwnership(ctx, manifest, env)
		if inspectionErr != nil {
			return errors.New("cannot safely confirm service name availability")
		}
		if registered {
			return errors.New("service name already exists")
		}
		if err = createServiceDirectory(base, dir); err != nil {
			return err
		}
		if err = writePrivate(manifestPath, mustServiceJSON(manifest)); err != nil {
			return err
		}
		if err = writePrivate(manifest.Registration, []byte(registration)); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(manifest.Resource), 0700); err != nil {
			return err
		}
		if err = writePrivate(manifest.Resource, []byte(definition)); err != nil {
			return err
		}
		switch env.os {
		case "linux":
			if _, err = env.run(ctx, "systemctl", "--user", "daemon-reload"); err == nil && manifest.Autostart {
				_, err = env.run(ctx, "systemctl", "--user", "enable", serviceUnit(manifest.Name))
			}
		case "darwin":
			// Registration is the owned plist; loading is deferred to explicit start or login.
			err = nil
		case "windows":
			_, err = env.run(ctx, "schtasks.exe", "/Create", "/TN", serviceLabel(manifest.Name), "/XML", manifest.Resource)
		}
		if err != nil {
			return errors.New("user service registration failed; owned files retained for review and uninstall")
		}
	} else {
		if err = verifyServiceOwnership(manifest, env); err != nil {
			return err
		}
		if err = serviceManagerAvailable(ctx, env); err != nil {
			return err
		}
		registered, e := verifyServiceManagerOwnership(ctx, manifest, env)
		if e != nil {
			return e
		}
		if !registered && action == "start" && env.os != "darwin" {
			return errors.New("service is not registered; reinstall it explicitly")
		}
		if !registered && action == "status" {
			if *machine {
				return json.NewEncoder(stdout).Encode(map[string]any{"name": manifest.Name, "registered": false, "status": "not_registered"})
			}
			_, e = fmt.Fprintln(stdout, "Service is not registered.")
			return e
		}
		if (action == "stop" || action == "uninstall") && env.stopHost != nil {
			graceful, err = env.stopHost(ctx, manifest.TicketID)
			if err != nil {
				return err
			}
		}
		if registered || action == "start" {
			switch env.os {
			case "linux":
				args := []string{"--user", action, serviceUnit(manifest.Name)}
				if action == "uninstall" {
					if _, err = env.run(ctx, "systemctl", "--user", "stop", serviceUnit(manifest.Name)); err != nil {
						return errors.New("cannot stop owned user service")
					}
					if manifest.Autostart {
						if _, err = env.run(ctx, "systemctl", "--user", "disable", serviceUnit(manifest.Name)); err != nil {
							return errors.New("cannot disable owned user service")
						}
					}
				} else {
					output, e := env.run(ctx, "systemctl", args...)
					err = e
					if action == "status" {
						return printServiceStatus(stdout, *machine, manifest, string(output), err)
					}
				}
			case "darwin":
				domain := "gui/" + env.uid + "/" + serviceLabel(manifest.Name)
				switch action {
				case "start":
					if output, e := env.run(ctx, "launchctl", "print", domain); e != nil {
						_, err = env.run(ctx, "launchctl", "bootstrap", "gui/"+env.uid, manifest.Resource)
					} else if serviceLaunchctlPath(output, manifest.Resource) != nil {
						return errors.New("launch agent label belongs to another resource")
					}
					if err == nil {
						_, err = env.run(ctx, "launchctl", "kickstart", domain)
					}
				case "status":
					output, e := env.run(ctx, "launchctl", "print", domain)
					if e != nil {
						return printServiceStatus(stdout, *machine, manifest, "not_loaded", nil)
					}
					return printServiceStatus(stdout, *machine, manifest, string(output), e)
				case "stop", "uninstall":
					if output, e := env.run(ctx, "launchctl", "print", domain); e == nil {
						if serviceLaunchctlPath(output, manifest.Resource) != nil {
							return errors.New("launch agent label belongs to another resource")
						}
						_, err = env.run(ctx, "launchctl", "bootout", domain)
					}
				}
			case "windows":
				if action == "stop" && graceful {
					break
				}
				if action == "uninstall" && !graceful {
					if _, err = env.run(ctx, "schtasks.exe", "/End", "/TN", serviceLabel(manifest.Name)); err != nil {
						script := `$s=New-Object -ComObject Schedule.Service; $s.Connect(); $s.GetFolder('\').GetTask('` + serviceLabel(manifest.Name) + `').State`
						state, e := env.run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
						value := strings.TrimSpace(string(state))
						if e != nil || value != "1" && value != "2" && value != "3" {
							return errors.New("cannot confirm scheduled host stopped; stop it in Task Scheduler before uninstall")
						}
					}
				}
				op := "/Run"
				switch action {
				case "stop":
					op = "/End"
				case "uninstall":
					op = "/Delete"
				case "status":
					op = "/Query"
				}
				args := []string{op, "/TN", serviceLabel(manifest.Name)}
				if action == "uninstall" {
					args = append(args, "/F")
				}
				output, e := env.run(ctx, "schtasks.exe", args...)
				err = e
				if action == "status" {
					return printServiceStatus(stdout, *machine, manifest, string(output), err)
				}
			}
		}
		if err != nil {
			return errors.New("user service manager operation failed")
		}
		if action == "uninstall" {
			if err = os.Remove(manifest.Resource); err != nil {
				return err
			}
			if err = os.Remove(manifest.Registration); err != nil {
				return err
			}
			if err = os.Remove(manifestPath); err != nil {
				return err
			}
			_ = os.Remove(dir)
			if env.os == "linux" {
				if _, err = env.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
					return errors.New("files removed but user manager reload failed")
				}
			}
		}
	}
	if action == "stop" || action == "uninstall" {
		if *machine {
			return json.NewEncoder(stdout).Encode(map[string]any{"action": action, "name": manifest.Name, "local_host_stopped": graceful, "revocation_confirmed": false, "ticket_deleted": false})
		}
		if _, err := fmt.Fprintf(stdout, "%s %s completed. Ticket file preserved.\n", action, manifest.Name); err != nil {
			return err
		}
		if !graceful {
			_, err = fmt.Fprintln(stdout, "Relay revocation was not confirmed. Use shellpier revoke with the host ticket to confirm withdrawal.")
		}
		return err
	}
	return printServicePlan(stdout, *machine, action, manifest, "", false)
}
func serviceHostTicket(path string) error {
	if err := checkPrivateSessionFile(path); err != nil {
		return errors.New("service ticket must be a private owned regular file")
	}
	ticket, err := access.Read("@" + path)
	if err != nil {
		return errors.New("service host ticket is invalid, expired, or unreadable")
	}
	if err = ticket.Validate("host"); err != nil {
		return errors.New("service requires an unexpired host ticket")
	}
	return nil
}
func serviceName(name string) bool {
	if len(name) < 1 || len(name) > 48 {
		return false
	}
	for i, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_')) {
			return false
		}
	}
	return true
}
func serviceLabel(name string) string { return "com.shellpier.host." + name }
func serviceUnit(name string) string  { return "shellpier-host-" + name + ".service" }
func serviceExtension(osname string) string {
	switch osname {
	case "darwin":
		return ".plist"
	case "windows":
		return ".xml"
	default:
		return ".service"
	}
}
func serviceResource(env serviceEnvironment, name string, autostart bool, dir string) string {
	switch env.os {
	case "darwin":
		if !autostart {
			return filepath.Join(dir, "agent.plist")
		}
		return filepath.Join(env.home, "Library", "LaunchAgents", serviceLabel(name)+".plist")
	case "linux":
		return filepath.Join(env.home, ".config", "systemd", "user", serviceUnit(name))
	default:
		return filepath.Join(env.home, "AppData", "Local", "ShellPier", "Services", "shellpier-host-"+name+".xml")
	}
}
func serviceArgs(m serviceManifest) []string {
	args := []string{"host", "@" + m.TicketFile, "--shell", m.Shell}
	if m.WorkDir != "" {
		args = append(args, "--work-dir", m.WorkDir)
	}
	if m.AllowJobs {
		args = append(args, "--allow-jobs")
	}
	for _, target := range m.AllowForward {
		args = append(args, "--allow-forward", target)
	}
	if m.KeepAwake {
		args = append(args, "--keep-awake")
	}
	return args
}
func serviceXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func serviceSystemdArg(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "$", "$$")
	return "\"" + s + "\""
}
func serviceWindowsArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range s {
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			b.WriteString(strings.Repeat("\\", slashes*2+1))
			b.WriteRune(c)
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
			b.WriteRune(c)
		}
		slashes = 0
	}
	b.WriteString(strings.Repeat("\\", slashes*2))
	b.WriteByte('"')
	return b.String()
}
func renderService(m serviceManifest, env serviceEnvironment, registration bool) string {
	args := serviceArgs(m)
	switch env.os {
	case "linux":
		items := []string{serviceSystemdArg(m.Program)}
		for _, arg := range args {
			items = append(items, serviceSystemdArg(arg))
		}
		return "[Unit]\nDescription=ShellPier " + m.Name + "\n[Service]\nType=simple\nExecStart=" + strings.Join(items, " ") + "\nRestart=no\n[Install]\nWantedBy=default.target\n"
	case "darwin":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>` + serviceLabel(m.Name) + `</string><key>ProgramArguments</key><array>`)
		for _, arg := range append([]string{m.Program}, args...) {
			b.WriteString("<string>" + serviceXML(arg) + "</string>")
		}
		b.WriteString("</array><key>KeepAlive</key><false/><key>RunAtLoad</key>")
		if m.Autostart {
			b.WriteString("<true/>")
		} else {
			b.WriteString("<false/>")
		}
		b.WriteString("</dict></plist>\n")
		return b.String()
	default:
		items := []string{}
		for _, arg := range args {
			items = append(items, serviceWindowsArg(arg))
		}
		trigger := "<Triggers/>"
		if m.Autostart {
			trigger = "<Triggers><LogonTrigger><Enabled>true</Enabled><UserId>" + serviceXML(env.uid) + "</UserId></LogonTrigger></Triggers>"
		}
		return `<?xml version="1.0" encoding="UTF-8"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><RegistrationInfo><Description>` + m.Marker + `</Description></RegistrationInfo>` + trigger + `<Principals><Principal id="User"><UserId>` + serviceXML(env.uid) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><Enabled>true</Enabled></Settings><Actions Context="User"><Exec><Command>` + serviceXML(m.Program) + `</Command><Arguments>` + serviceXML(strings.Join(items, " ")) + `</Arguments></Exec></Actions></Task>`
	}
}
func createServiceDirectory(base, dir string) error {
	if err := os.MkdirAll(filepath.Dir(base), 0700); err != nil {
		return err
	}
	for _, folder := range []string{base, filepath.Join(base, "services"), dir} {
		if err := os.Mkdir(folder, 0700); err == nil {
			if err = platform.SecureDir(folder); err != nil {
				return err
			}
		} else if !os.IsExist(err) {
			return err
		}
		if err := checkPrivateSessionDir(folder); err != nil {
			return err
		}
	}
	return nil
}

func mustServiceJSON(m serviceManifest) []byte {
	data, _ := json.MarshalIndent(m, "", "  ")
	return append(data, '\n')
}
func readServiceManifest(path string, env serviceEnvironment, name string) (serviceManifest, error) {
	var m serviceManifest
	if err := checkPrivateSessionDir(filepath.Dir(path)); err != nil {
		return m, err
	}
	if err := checkPrivateSessionFile(path); err != nil {
		return m, err
	}
	data, err := readRegular(path, 32768)
	if err != nil {
		return m, err
	}
	if json.Unmarshal(data, &m) != nil || m.Version != 1 || m.Name != name || m.OS != env.os || len(m.Marker) != 32 || m.Resource != serviceResource(env, name, m.Autostart, filepath.Dir(path)) || m.Registration != filepath.Join(filepath.Dir(path), "registration"+serviceExtension(env.os)) || !filepath.IsAbs(m.Program) || !filepath.IsAbs(m.TicketFile) {
		return m, errors.New("invalid service ownership manifest")
	}
	return m, nil
}
func verifyServiceOwnership(m serviceManifest, env serviceEnvironment) error {
	for path, expected := range map[string]string{m.Resource: renderService(m, env, false), m.Registration: renderService(m, env, true)} {
		if err := checkPrivateSessionFile(path); err != nil {
			return err
		}
		actual, err := readRegular(path, 32768)
		if err != nil || string(actual) != expected {
			return errors.New("service configuration changed; refusing to control or remove it")
		}
	}
	return nil
}
func serviceManagerAvailable(ctx context.Context, env serviceEnvironment) error {
	var err error
	switch env.os {
	case "linux":
		_, err = env.run(ctx, "systemctl", "--user", "show-environment")
	case "darwin":
		_, err = env.run(ctx, "launchctl", "print", "gui/"+env.uid)
	case "windows":
		_, err = env.run(ctx, "schtasks.exe", "/Query")
	}
	if err != nil {
		return errors.New("current-user service manager is unavailable")
	}
	return nil
}
func printServicePlan(out io.Writer, machine bool, action string, m serviceManifest, definition string, dry bool) error {
	if machine {
		return json.NewEncoder(out).Encode(struct {
			Action     string          `json:"action"`
			DryRun     bool            `json:"dry_run"`
			Service    serviceManifest `json:"service"`
			Definition string          `json:"definition,omitempty"`
		}{action, dry, m, definition})
	}
	_, err := fmt.Fprintf(out, "%s %s\nResource: %s\nTicket: %s\nAutostart at login: %t\n%s", action, m.Name, m.Resource, m.TicketFile, m.Autostart, definition)
	return err
}
func printServiceStatus(out io.Writer, machine bool, m serviceManifest, status string, operationErr error) error {
	if machine {
		if err := json.NewEncoder(out).Encode(map[string]any{"name": m.Name, "registered": true, "manager_ok": operationErr == nil, "status": strings.TrimSpace(status)}); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(out, strings.TrimSpace(status)); err != nil {
			return err
		}
	}
	return operationErr
}

type serviceStrings []string

func (s *serviceStrings) String() string         { return strings.Join(*s, ",") }
func (s *serviceStrings) Set(value string) error { *s = append(*s, value); return nil }

func verifyServiceManagerOwnership(ctx context.Context, m serviceManifest, env serviceEnvironment) (bool, error) {
	switch env.os {
	case "linux":
		output, err := env.run(ctx, "systemctl", "--user", "show", "--property=FragmentPath", "--value", serviceUnit(m.Name))
		if err != nil {
			return false, errors.New("cannot inspect user unit registration")
		}
		fragment := strings.TrimSpace(string(output))
		if fragment == "" {
			state, e := env.run(ctx, "systemctl", "--user", "show", "--property=LoadState", "--value", serviceUnit(m.Name))
			if e == nil && strings.TrimSpace(string(state)) == "not-found" {
				return false, nil
			}
			return false, errors.New("user unit absence cannot be confirmed")
		}
		if fragment != m.Resource {
			return false, errors.New("user unit is not registered from the owned resource")
		}
	case "darwin":
		output, err := env.run(ctx, "launchctl", "print", "gui/"+env.uid+"/"+serviceLabel(m.Name))
		if err != nil {
			if strings.Contains(string(output), `Could not find service "`+serviceLabel(m.Name)+`"`) {
				return false, nil
			}
			return false, errors.New("launch agent absence cannot be confirmed")
		}
		if serviceLaunchctlPath(output, m.Resource) != nil {
			return false, errors.New("launch agent label belongs to another resource")
		}
	case "windows":
		output, err := env.run(ctx, "schtasks.exe", "/Query", "/TN", serviceLabel(m.Name), "/XML")
		if err != nil {
			script := `$ErrorActionPreference='Stop'; $s=New-Object -ComObject Schedule.Service; $s.Connect(); try { $t=$s.GetFolder('\').GetTask('` + serviceLabel(m.Name) + `'); [Console]::Write('SHELLPIER_REGISTERED') } catch { $e=$_.Exception; while($e.InnerException){$e=$e.InnerException}; if($e.HResult -eq -2147024894){[Console]::Write('SHELLPIER_NOT_REGISTERED')}else{throw} }`
			probe, e := env.run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
			if e == nil && strings.TrimSpace(string(probe)) == "SHELLPIER_NOT_REGISTERED" {
				return false, nil
			}
			return false, errors.New("scheduled task absence cannot be confirmed")
		}
		var task struct {
			Registration struct {
				Description string `xml:"Description"`
			} `xml:"RegistrationInfo"`
			Actions struct {
				Exec struct {
					Command   string `xml:"Command"`
					Arguments string `xml:"Arguments"`
				} `xml:"Exec"`
			} `xml:"Actions"`
		}
		if xml.Unmarshal(serviceXMLBytes(output), &task) != nil || task.Registration.Description != m.Marker || task.Actions.Exec.Command != m.Program {
			return false, errors.New("scheduled task ownership cannot be verified")
		}
		args := []string{}
		for _, arg := range serviceArgs(m) {
			args = append(args, serviceWindowsArg(arg))
		}
		if task.Actions.Exec.Arguments != strings.Join(args, " ") {
			return false, errors.New("scheduled task arguments changed")
		}
	}
	return true, nil
}

// Task Scheduler may emit UTF-16 XML even when the process pipe contains bytes.
func serviceXMLBytes(data []byte) []byte {
	if len(data) >= 2 && (data[0] == 0xff && data[1] == 0xfe || data[0] == 0xfe && data[1] == 0xff) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		units := make([]uint16, 0, (len(data)-2)/2)
		for i := 2; i+1 < len(data); i += 2 {
			units = append(units, order.Uint16(data[i:i+2]))
		}
		data = []byte(string(utf16.Decode(units)))
	}
	text := string(data)
	text = strings.ReplaceAll(text, `encoding="UTF-16"`, `encoding="UTF-8"`)
	text = strings.ReplaceAll(text, `encoding="utf-16"`, `encoding="UTF-8"`)
	return []byte(text)
}

// serviceStopLocal uses the host control endpoint so normal stop can revoke its relay session.
func serviceStopLocal(ctx context.Context, id string) (bool, error) {
	var buffer bytes.Buffer
	if err := sessions(ctx, []string{"--json"}, &buffer, io.Discard); err != nil {
		return false, err
	}
	var result struct {
		Sessions  []sessionEntry `json:"sessions"`
		Truncated bool           `json:"truncated"`
	}
	if json.Unmarshal(buffer.Bytes(), &result) != nil || result.Truncated {
		return false, errors.New("cannot completely inspect local host sessions")
	}
	stopped := false
	for _, entry := range result.Sessions {
		if entry.ID == id && entry.Alive {
			if err := control(ctx, "stop", []string{"--state", entry.StatePath}, io.Discard, io.Discard); err != nil {
				return stopped, err
			}
			stopped = true
		}
	}
	return stopped, nil
}

// serviceLaunchctlPath compares the job's dedicated path field to the same owned file.
// launchd canonicalizes parent aliases such as /var to /private/var on macOS.
func serviceLaunchctlPath(output []byte, expected string) error {
	actual := ""
	found := false
	for _, line := range strings.Split(string(output), "\n") {
		value, ok := strings.CutPrefix(strings.TrimLeft(strings.TrimSuffix(line, "\r"), " \t"), "path = ")
		if !ok {
			continue
		}
		if found {
			return errors.New("launch agent has multiple path fields")
		}
		found = true
		actual = value
	}
	if actual == "" || !filepath.IsAbs(actual) || strings.ContainsRune(actual, 0) {
		return errors.New("launch agent has no valid path field")
	}
	if err := checkPrivateSessionFile(expected); err != nil {
		return err
	}
	expectedCanonical, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return err
	}
	actualCanonical, err := filepath.EvalSymlinks(actual)
	if err != nil {
		return err
	}
	if actualCanonical != expectedCanonical {
		return errors.New("launch agent references another configuration")
	}
	owned, err := os.Stat(expected)
	if err != nil {
		return err
	}
	registered, err := os.Stat(actual)
	if err != nil {
		return err
	}
	if !os.SameFile(owned, registered) {
		return errors.New("launch agent configuration changed")
	}
	return nil
}
