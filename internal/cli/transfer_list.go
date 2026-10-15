package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

const transferListMaxBytes = 8 * 1024 * 1024

func readTransferList(ctx context.Context, filename string, stdin io.Reader, from0 bool, maxFiles, maxDepth int) ([]string, error) {
	input := stdin
	if filename != "-" {
		file, err := openTransferSource(filename)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		input = file
	}
	if input == nil {
		return nil, &UsageError{Err: errors.New("files-from - requires standard input")}
	}
	stop := context.AfterFunc(ctx, func() {
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stop()
	reader := newStreamInput(ctx, input)
	defer close(reader.stop)
	data, err := io.ReadAll(io.LimitReader(reader, transferListMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > transferListMaxBytes {
		return nil, &UsageError{Err: errors.New("files-from exceeds 8 MiB")}
	}
	separator := byte('\n')
	if from0 {
		separator = 0
	} else if bytes.IndexByte(data, 0) >= 0 {
		return nil, &UsageError{Err: errors.New("NUL in line-delimited files-from; use from0")}
	}
	seen := make(map[string]string)
	var names []string
	for _, record := range bytes.Split(data, []byte{separator}) {
		if !from0 && len(record) > 0 && record[len(record)-1] == '\r' {
			record = record[:len(record)-1]
		}
		if len(record) == 0 {
			continue
		}
		name := string(record)
		for strings.HasPrefix(name, "./") {
			name = strings.TrimPrefix(name, "./")
		}
		if len(name) > 4096 || name == "" || strings.ContainsAny(name, "\\\x00") {
			return nil, &UsageError{Err: fmt.Errorf("invalid files-from path %q", name)}
		}
		components := strings.Split(name, "/")
		if len(components)-1 > maxDepth {
			return nil, &UsageError{Err: errors.New("files-from exceeds max-depth")}
		}
		for _, component := range components {
			if !safeTransferName(component) {
				return nil, &UsageError{Err: fmt.Errorf("unsafe files-from path %q", name)}
			}
		}
		folded := strings.ToLower(name)
		if previous, ok := seen[folded]; ok {
			if previous != name {
				return nil, &UsageError{Err: errors.New("case-insensitive files-from name collision")}
			}
			continue
		}
		seen[folded] = name
		names = append(names, name)
		if len(names) > maxFiles {
			return nil, &UsageError{Err: errors.New("files-from exceeds max-files")}
		}
	}
	// Component-level collisions include e.g. A/one and a/two.
	components := make(map[string]string)
	for _, name := range names {
		for prefix := name; prefix != "."; prefix = path.Dir(prefix) {
			key := strings.ToLower(prefix)
			if previous, ok := components[key]; ok && previous != prefix {
				return nil, &UsageError{Err: errors.New("case-insensitive files-from component collision")}
			}
			components[key] = prefix
			if len(components) > maxFiles+maxDepth+10000 {
				return nil, &UsageError{Err: errors.New("files-from entry capacity exceeded")}
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// Check each component without canonicalising away a selected symlink. Missing
// destination suffixes are allowed because their directories will be created.
func checkTransferListDirectory(fs transferFS, root, relative string, missingOK bool) error {
	current := root
	parts := []string{}
	if relative != "" && relative != "." {
		parts = strings.Split(relative, "/")
	}
	for i := 0; ; i++ {
		info, err := fs.lstat(current)
		if err != nil {
			if missingOK && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("files-from directory is not a real directory: %s", current)
		}
		if i == len(parts) {
			return nil
		}
		current = fs.join(current, parts[i])
	}
}

func checkTransferListPaths(src, dst transferFS, source, destination, relative string) error {
	parent := path.Dir(relative)
	if err := checkTransferListDirectory(src, source, parent, false); err != nil {
		return err
	}
	return checkTransferListDirectory(dst, destination, parent, true)
}

func runTransferList(ctx context.Context, src, dst transferFS, source, destination string, names []string, progress func(int64) error) (transferSummary, error) {
	// Joining an empty suffix strips trailing separators before Lstat, which
	// otherwise can follow a directory symlink on some operating systems.
	source = src.join(source, "")
	destination = dst.join(destination, "")
	if len(names) == 0 {
		return transferSummary{DryRun: transferSettings(ctx).dryRun}, nil
	}
	if err := checkTransferListDirectory(src, source, "", false); err != nil {
		return transferSummary{}, err
	}
	root, err := src.lstat(source)
	if err != nil {
		return transferSummary{}, err
	}
	plan := []transferTreeEntry{{directory: true, original: root}}
	dirs := map[string]bool{"": true}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return transferSummary{}, err
		}
		if err := checkTransferListPaths(src, dst, source, destination, name); err != nil {
			return transferSummary{}, err
		}
		info, err := src.lstat(src.join(source, name))
		if err != nil {
			return transferSummary{}, err
		}
		if !info.Mode().IsRegular() {
			return transferSummary{}, fmt.Errorf("files-from requires a regular file: %s", name)
		}
		parent := path.Dir(name)
		var missing []string
		for parent != "." && !dirs[parent] {
			missing = append(missing, parent)
			parent = path.Dir(parent)
		}
		for i := len(missing) - 1; i >= 0; i-- {
			rel := missing[i]
			directory, err := src.lstat(src.join(source, rel))
			if err != nil {
				return transferSummary{}, err
			}
			if len(plan) >= 2*len(names)+10000 {
				return transferSummary{}, errors.New("files-from plan entry capacity exceeded")
			}
			dirs[rel] = true
			plan = append(plan, transferTreeEntry{relative: rel, directory: true, original: directory})
		}
		plan = append(plan, transferTreeEntry{relative: name, original: info})
	}
	return runTransferPlan(ctx, src, dst, source, destination, plan, true, progress)
}
