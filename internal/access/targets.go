package access

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
)

var targetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type targetReference struct {
	Version    int    `json:"version"`
	TicketFile string `json:"ticket_file"`
}

// TargetInfo never contains ticket keys or authentication tokens.
type TargetInfo struct {
	Name       string    `json:"name"`
	TicketFile string    `json:"ticket_file,omitempty"`
	Status     string    `json:"status"`
	ID         string    `json:"id,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	Transport  string    `json:"transport,omitempty"`
}

func TargetConfigDir(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv("SHELLPIER_CONFIG_DIR")
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "shellpier")
	}
	if strings.ContainsRune(dir, 0) {
		return "", errors.New("invalid config directory")
	}
	return filepath.Abs(dir)
}

func validTargetName(name string) error {
	upper := strings.ToUpper(name)
	reserved := upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" || len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '0' && upper[3] <= '9'
	if !targetName.MatchString(name) || reserved {
		return errors.New("target name must be 1..64 ASCII letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	return nil
}

// openTargets anchors operations to checked private directories, including after rename.
func openTargets(dir string, create bool) (*os.Root, func() error, error) {
	base, err := TargetConfigDir(dir)
	if err != nil {
		return nil, nil, err
	}
	if create {
		if err = os.MkdirAll(filepath.Dir(base), 0700); err != nil {
			return nil, nil, err
		}
		if err = os.Mkdir(base, 0700); err == nil {
			err = platform.SecureDir(base)
		} else if os.IsExist(err) {
			err = nil
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if err = checkTargetPrivateDir(base); err != nil {
		return nil, nil, err
	}
	baseInfo, err := os.Stat(base)
	if err != nil {
		return nil, nil, err
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, nil, err
	}
	defer baseRoot.Close()
	if create {
		if err = baseRoot.Mkdir("targets", 0700); err == nil {
			err = platform.SecureDir(filepath.Join(base, "targets"))
		} else if os.IsExist(err) {
			err = nil
		}
		if err != nil {
			return nil, nil, err
		}
	}
	folder := filepath.Join(base, "targets")
	if err = checkTargetPrivateDir(folder); err != nil {
		return nil, nil, err
	}
	info, err := baseRoot.Lstat("targets")
	if err != nil {
		return nil, nil, err
	}
	root, err := baseRoot.OpenRoot("targets")
	if err != nil {
		return nil, nil, err
	}
	verify := func() error {
		if err := checkTargetPrivateDir(base); err != nil {
			return err
		}
		current, err := os.Stat(base)
		if err != nil {
			return err
		}
		if !os.SameFile(baseInfo, current) {
			return errors.New("config directory changed")
		}
		if err = checkTargetPrivateDir(folder); err != nil {
			return err
		}
		current, err = os.Stat(folder)
		if err != nil {
			return err
		}
		if !os.SameFile(info, current) {
			return errors.New("targets directory changed")
		}
		return nil
	}
	if err = verify(); err != nil {
		root.Close()
		return nil, nil, err
	}
	return root, verify, nil
}

func readTargetReference(root *os.Root, name string) (targetReference, error) {
	var ref targetReference
	if err := validTargetName(name); err != nil {
		return ref, err
	}
	filename := name + ".json"
	if err := checkTargetPrivateFile(filepath.Join(root.Name(), filename)); err != nil {
		return ref, err
	}
	before, err := root.Lstat(filename)
	if err != nil {
		return ref, err
	}
	file, err := root.Open(filename)
	if err != nil {
		return ref, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return ref, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return ref, errors.New("target reference changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return ref, err
	}
	if len(data) > 8192 {
		return ref, errors.New("target reference too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ref) != nil || decoder.Decode(new(any)) != io.EOF || ref.Version != 1 || !filepath.IsAbs(ref.TicketFile) || strings.ContainsRune(ref.TicketFile, 0) || len(ref.TicketFile) > 4096 {
		return ref, errors.New("invalid target reference")
	}
	return ref, nil
}

func ResolveTarget(dir, name string) (string, error) {
	if err := validTargetName(name); err != nil {
		return "", err
	}
	root, verify, err := openTargets(dir, false)
	if err != nil {
		return "", err
	}
	defer root.Close()
	ref, err := readTargetReference(root, name)
	if err != nil {
		return "", err
	}
	if err = checkTargetPrivateFile(ref.TicketFile); err != nil {
		return "", err
	}
	if err = verify(); err != nil {
		return "", err
	}
	return ref.TicketFile, nil
}

func AddTarget(dir, name, file string) (TargetInfo, error) {
	result := TargetInfo{Name: name}
	if err := validTargetName(name); err != nil {
		return result, err
	}
	if file == "" || strings.HasPrefix(file, "target:") || strings.HasPrefix(file, "sp1_") {
		return result, errors.New("provide a private ticket file, not a token or target alias")
	}
	absolute, err := filepath.Abs(strings.TrimPrefix(file, "@"))
	if err != nil {
		return result, err
	}
	if len(absolute) > 4096 {
		return result, errors.New("ticket path exceeds 4096 bytes")
	}
	if err = checkTargetPrivateFile(absolute); err != nil {
		return result, err
	}
	ticket, err := Read("@" + absolute)
	if err != nil {
		return result, err
	}
	if err = ticket.Validate(""); err != nil {
		return result, err
	}
	root, verify, err := openTargets(dir, true)
	if err != nil {
		return result, err
	}
	defer root.Close()
	nameFile := name + ".json"
	output, err := root.OpenFile(nameFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	owned, statErr := output.Stat()
	if statErr != nil {
		output.Close()
		return result, statErr
	}
	written := false
	defer func() {
		if !written {
			current, e := root.Lstat(nameFile)
			if e == nil && os.SameFile(owned, current) {
				_ = root.Remove(nameFile)
			}
		}
	}()
	// O_EXCL never replaces an existing alias, including symlinks.
	if err = platform.SecureFile(filepath.Join(root.Name(), nameFile)); err == nil {
		err = json.NewEncoder(output).Encode(targetReference{1, absolute})
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return result, err
	}
	if err = verify(); err != nil {
		return result, err
	}
	written = true
	return ShowTarget(dir, name)
}

func ShowTarget(dir, name string) (TargetInfo, error) {
	result := TargetInfo{Name: name, Status: "invalid"}
	if err := validTargetName(name); err != nil {
		return result, err
	}
	root, verify, err := openTargets(dir, false)
	if err != nil {
		return result, err
	}
	defer root.Close()
	ref, err := readTargetReference(root, name)
	if err != nil {
		return result, err
	}
	result.TicketFile = ref.TicketFile
	if err = checkTargetPrivateFile(ref.TicketFile); err != nil {
		if os.IsNotExist(err) {
			result.Status = "missing"
		} else {
			result.Status = "inaccessible"
		}
		return result, verify()
	}
	data, err := ReadFile(ref.TicketFile, maxTicket)
	if err != nil {
		result.Status = "unreadable"
		return result, verify()
	}
	data = bytes.TrimSpace(data)
	if bytes.HasPrefix(data, []byte("sp1_")) {
		data, err = base64.RawURLEncoding.Strict().DecodeString(string(data[4:]))
		if err != nil {
			return result, verify()
		}
	}
	var ticket Ticket
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ticket) != nil || decoder.Decode(new(any)) != io.EOF {
		return result, verify()
	}
	expiry := ticket.ExpiresAt
	ticket.ExpiresAt = time.Time{}
	if ticket.configured() != nil {
		return result, verify()
	}
	result.ID, result.Kind, result.ExpiresAt = ticket.ID, ticket.Kind, expiry
	result.Transport = "direct"
	if ticket.RelayURL != "" {
		result.Transport = "relay"
	}
	result.Status = "active"
	if !expiry.IsZero() && !time.Now().Before(expiry) {
		result.Status = "expired"
	}
	return result, verify()
}

func ListTargets(dir string) ([]TargetInfo, error) {
	result := []TargetInfo{}
	root, verify, err := openTargets(dir, false)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	folder, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	entries, err := folder.ReadDir(10001)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, errors.New("too many target references")
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if entry.Name() == name || validTargetName(name) != nil {
			continue
		}
		item, err := ShowTarget(dir, name)
		if err != nil {
			item = TargetInfo{Name: name, Status: "invalid"}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, verify()
}

func RemoveTarget(dir, name string) error {
	if err := validTargetName(name); err != nil {
		return err
	}
	root, verify, err := openTargets(dir, false)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(name + ".json")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("target reference is not a regular file")
	}
	if err = verify(); err != nil {
		return err
	}
	if err = root.Remove(name + ".json"); err != nil {
		return fmt.Errorf("remove target reference: %w", err)
	}
	return verify()
}
