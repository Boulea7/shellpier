package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/pkg/sftp"
)

const filesMaxRead = 16 * 1024 * 1024

type filesMetadata struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
	Symlink bool      `json:"symlink"`
}

func filesInfo(p string, info os.FileInfo) filesMetadata {
	return filesMetadata{p, info.Name(), info.Size(), info.Mode().String(), info.ModTime(), info.IsDir(), info.Mode()&os.ModeSymlink != 0}
}

func files(parent context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (result error) {
	_ = stdin
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier files list|stat|read|mkdir|rename|remove [flags] TICKET PATH [DEST]\nMetadata supports --json. read prints raw bytes or base64 JSON.\nMutations support --dry-run. mkdir supports --parents. rename refuses existing targets; its existence check is not atomic on SFTP v3. remove never recurses.")
		return err
	}
	action := args[0]
	switch action {
	case "list", "stat", "read", "mkdir", "rename", "remove":
	default:
		return fmt.Errorf("unknown files action %q", action)
	}
	f := flags("files "+action, stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 5*time.Minute, "total file operation timeout")
	asJSON := f.Bool("json", false, "emit metadata or base64 read data as JSON")
	mutation := action == "mkdir" || action == "rename" || action == "remove"
	var dryRun, parents bool
	if mutation {
		f.BoolVar(&dryRun, "dry-run", false, "validate and describe without writing")
	}
	if action == "mkdir" {
		f.BoolVar(&parents, "parents", false, "create missing parent directories")
	}
	var limit, offset int64
	if action == "list" {
		f.Int64Var(&limit, "limit", 1000, "maximum entries, 1..10000")
	}
	if action == "read" {
		f.Int64Var(&limit, "limit", 1024*1024, "maximum bytes, 1..16777216")
		f.Int64Var(&offset, "offset", 0, "starting byte offset")
	}
	f.Usage = func() {
		suffix := ""
		if action == "rename" {
			suffix = " DEST"
		}
		fmt.Fprintf(stderr, "Usage: shellpier files %s [flags] TICKET PATH%s\n", action, suffix)
		f.PrintDefaults()
	}
	if err := parseFlags(f, args[1:]); err != nil {
		return err
	}
	expected := 2
	if action == "rename" {
		expected = 3
	}
	if f.NArg() != expected {
		return fmt.Errorf("usage: shellpier files %s [flags] TICKET PATH%s", action, map[bool]string{true: " DEST", false: ""}[action == "rename"])
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if action == "list" && (limit < 1 || limit > 10000) {
		return errors.New("list limit must be 1..10000")
	}
	if action == "read" && (limit < 1 || limit > filesMaxRead || offset < 0 || offset > math.MaxInt64-limit) {
		return errors.New("read requires offset >= 0 and limit 1..16777216 without overflow")
	}
	for _, p := range f.Args()[1:] {
		if p == "" || len(p) > 4096 || strings.ContainsRune(p, 0) {
			return errors.New("path must be nonempty, at most 4096 bytes, and contain no NUL")
		}
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	ticket, err := access.Read(f.Arg(0))
	if err != nil {
		return err
	}
	if err = ticket.Validate("access"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			result = ctx.Err()
		}
	}()
	client, err := access.DialRetry(ctx, ticket, *iface, *retry)
	if err != nil {
		return err
	}
	defer client.Close()
	sf, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sf.Close()
	p := f.Arg(1)
	switch action {
	case "stat":
		info, err := sf.Lstat(p)
		if err != nil {
			return err
		}
		return filesOutput(stdout, *asJSON, filesInfo(p, info))
	case "list":
		entries := make([]filesMetadata, 0)
		listed, err := sf.ReadDirContext(ctx, p)
		if err != nil {
			return err
		}
		for i, info := range listed {
			if int64(i) > limit {
				break
			}
			entries = append(entries, filesInfo(path.Join(p, info.Name()), info))
		}
		hasMore := int64(len(entries)) > limit
		if hasMore {
			entries = entries[:limit]
		}
		if *asJSON {
			return json.NewEncoder(stdout).Encode(struct {
				Path    string          `json:"path"`
				Entries []filesMetadata `json:"entries"`
				HasMore bool            `json:"has_more"`
			}{p, entries, hasMore})
		}
		for _, entry := range entries {
			if err = filesOutput(stdout, false, entry); err != nil {
				return err
			}
		}
		if hasMore {
			_, err = fmt.Fprintln(stdout, "More entries available; increase --limit.")
		}
		return err
	case "read":
		return filesRead(sf, p, offset, limit, *asJSON, stdout)
	default:
		return filesMutation(sf, action, p, f.Args()[2:], parents, dryRun, *asJSON, stdout)
	}
}

func filesOutput(out io.Writer, asJSON bool, value filesMetadata) error {
	if asJSON {
		return json.NewEncoder(out).Encode(value)
	}
	_, err := fmt.Fprintf(out, "%s %10d %s %s\n", value.Mode, value.Size, value.ModTime.Format(time.RFC3339), value.Path)
	return err
}

func filesRead(sf *sftp.Client, p string, offset, limit int64, asJSON bool, out io.Writer) error {
	info, err := sf.Lstat(p)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("read requires a regular file")
	}
	file, err := sf.Open(p)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return err
	}
	hasMore := int64(len(data)) > limit
	if hasMore {
		data = data[:limit]
	}
	if asJSON {
		return json.NewEncoder(out).Encode(struct {
			Path     string `json:"path"`
			Offset   int64  `json:"offset"`
			Bytes    int    `json:"bytes"`
			Encoding string `json:"encoding"`
			Data     string `json:"data"`
			HasMore  bool   `json:"has_more"`
		}{p, offset, len(data), "base64", base64.StdEncoding.EncodeToString(data), hasMore})
	}
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func filesMutation(sf *sftp.Client, action, p string, dest []string, parents, dryRun, asJSON bool, out io.Writer) error {
	warning := ""
	target := ""
	switch action {
	case "mkdir":
		info, err := sf.Lstat(p)
		if err == nil {
			if !parents || !info.IsDir() {
				return errors.New("destination already exists")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = filesParentDirectory(sf, p, parents); err != nil {
			return err
		}

		if !dryRun {
			if parents {
				err = sf.MkdirAll(p)
			} else {
				err = sf.Mkdir(p)
			}
			if err != nil {
				return err
			}
		}
	case "rename":
		target = dest[0]
		if _, err := sf.Lstat(p); err != nil {
			return err
		}
		if _, err := sf.Lstat(target); err == nil {
			return errors.New("destination already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := filesParentDirectory(sf, target, false); err != nil {
			return err
		}
		warning = "SFTP v3 checks destination absence before rename; concurrent destination creation is not atomically excluded."
		if !dryRun {
			if err := sf.Rename(p, target); err != nil {
				return err
			}
		}
	case "remove":
		info, err := sf.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("remove requires a file, symlink, or empty directory")
		}
		if info.IsDir() {
			entries, err := sf.ReadDir(p)
			if err != nil {
				return err
			}
			if len(entries) > 0 {
				return errors.New("directory is not empty")
			}
		}
		if !dryRun {
			if info.IsDir() {
				err = sf.RemoveDirectory(p)
			} else {
				err = sf.Remove(p)
			}
			if err != nil {
				return err
			}
		}
	}
	response := struct {
		Action      string `json:"action"`
		Path        string `json:"path"`
		Destination string `json:"destination,omitempty"`
		DryRun      bool   `json:"dry_run"`
		Warning     string `json:"warning,omitempty"`
	}{action, p, target, dryRun, warning}
	if asJSON {
		return json.NewEncoder(out).Encode(response)
	}
	verb := "Completed"
	if dryRun {
		verb = "Would perform"
	}
	_, err := fmt.Fprintf(out, "%s %s: %s", verb, action, p)
	if err != nil {
		return err
	}
	if target != "" {
		if _, err = fmt.Fprintf(out, " -> %s", target); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintln(out); err != nil {
		return err
	}
	if warning != "" {
		_, err = fmt.Fprintln(out, warning)
	}
	return err
}

// filesParentDirectory validates the first existing ancestor without creating anything.
func filesParentDirectory(sf *sftp.Client, p string, parents bool) error {
	current := path.Dir(p)
	for {
		info, err := sf.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return errors.New("parent is not a directory")
			}
			return nil
		}
		if !parents || !os.IsNotExist(err) {
			return err
		}
		next := path.Dir(current)
		if next == current {
			return err
		}
		current = next
	}
}
