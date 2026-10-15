package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

type transferExclusions []string

func (e *transferExclusions) String() string { return strings.Join(*e, ",") }
func (e *transferExclusions) Set(s string) error {
	if s == "" || len(s) > 1024 || strings.Contains(s, "**") || strings.Contains(s, "\\") || strings.HasPrefix(s, "/") {
		return errors.New("exclude needs a relative slash glob without **")
	}
	if _, err := path.Match(s, ""); err != nil {
		return err
	}
	*e = append(*e, s)
	if len(*e) > 128 {
		return errors.New("at most 128 exclusion patterns")
	}
	return nil
}
func (e transferExclusions) matches(rel, name string) bool {
	for _, pattern := range e {
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if ok, _ := path.Match(pattern, name); ok {
				return true
			}
		}
	}
	return false
}

type transferTreeEntry struct {
	relative  string
	directory bool
	original  os.FileInfo
	action    string
	hash      string
}

func planTransferTree(ctx context.Context, fs transferFS, root string, exclude transferExclusions, maxFiles, maxDepth int) ([]transferTreeEntry, error) {
	info, e := fs.lstat(root)
	if e != nil {
		return nil, e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("recursive source must be a real directory")
	}
	plan := []transferTreeEntry{{directory: true, original: info}}
	files := 0
	var walk func(string, int) error
	walk = func(rel string, depth int) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		entries, e := fs.entries(fs.join(root, rel))
		if e != nil {
			return e
		}
		names := map[string]bool{}
		for _, entry := range entries {
			name := entry.Name()
			child := path.Join(rel, name)
			if exclude.matches(child, name) {
				continue
			}
			if !safeTransferName(name) {
				return fmt.Errorf("unsafe cross-platform entry name: %q", name)
			}
			fold := strings.ToLower(name)
			if names[fold] {
				return errors.New("case-insensitive destination name collision")
			}
			names[fold] = true
			if depth > maxDepth || entry.IsDir() && depth+1 > maxDepth {
				return errors.New("recursive transfer exceeds max-depth")
			}
			if entry.Mode()&os.ModeSymlink != 0 || !entry.IsDir() && !entry.Mode().IsRegular() {
				return errors.New("recursive transfer refuses symlinks and special files")
			}
			if len(plan) >= maxFiles+maxDepth+10000 {
				return errors.New("recursive transfer entry capacity exceeded")
			}
			plan = append(plan, transferTreeEntry{relative: child, directory: entry.IsDir(), original: entry})
			if entry.IsDir() {
				if e = walk(child, depth+1); e != nil {
					return e
				}
			} else {
				files++
				if files > maxFiles {
					return errors.New("recursive transfer exceeds max-files")
				}
			}
		}
		return nil
	}
	if e = walk("", 0); e != nil {
		return nil, e
	}
	return plan, nil
}
func runTransferTree(ctx context.Context, src, dst transferFS, source, destination string, exclude transferExclusions, maxFiles, maxDepth int, progress func(int64) error) (result transferSummary, err error) {
	plan, e := planTransferTree(ctx, src, source, exclude, maxFiles, maxDepth)
	if e != nil {
		return result, e
	}
	options := transferSettings(ctx)
	if e = transferParentDirectory(dst, destination); e != nil {
		return result, e
	}
	// Resolve every conflict before writing anything. Execution must still recheck
	// paths because a preview is not a filesystem lock or a snapshot.
	for i := range plan {
		item := &plan[i]
		from, to := src.join(source, item.relative), dst.join(destination, item.relative)
		current, e := src.lstat(from)
		if e != nil {
			return result, e
		}
		if item.directory {
			if !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
				return result, errors.New("source directory changed")
			}
		} else if !sameTransferSource(item.original, current) {
			return result, errors.New("source changed during tree planning")
		}
		existing, e := dst.lstat(to)
		if e != nil && !os.IsNotExist(e) {
			return result, e
		}
		if item.directory {
			item.action = "mkdir"
			if e == nil {
				if !options.skipIdentical || !existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
					return result, errors.New("destination already exists or is not a real directory")
				}
				item.action = "existing"
			}
			continue
		}
		item.action = "copy"
		if e == nil {
			if !options.skipIdentical {
				return result, errors.New("destination already exists")
			}
			same, e := identicalTransfer(ctx, src, dst, from, to)
			if e != nil {
				return result, e
			}
			if same {
				item.action = "skip"
			}
		}
		if options.verify || item.action == "skip" {
			snapshot, hash, e := transferSnapshot(ctx, src, from)
			if e != nil {
				return result, e
			}
			if !sameTransferSource(item.original, snapshot) {
				return result, errors.New("source changed during planning")
			}
			item.hash = hash
		}
	}
	if options.dryRun {
		result.DryRun = true
		for _, item := range plan {
			kind := "file"
			var size int64
			if item.directory {
				kind = "directory"
			} else {
				size = item.original.Size()
			}
			result.Plan = append(result.Plan, transferPlanItem{Path: item.relative, Kind: kind, Action: item.action, Bytes: size})
			if item.action == "copy" {
				result.PlannedBytes += size
			}
			if item.action == "skip" {
				result.SkippedFiles++
			}
		}
		return result, nil
	}
	for _, item := range plan {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		from, to := src.join(source, item.relative), dst.join(destination, item.relative)
		if item.directory {
			if item.action == "mkdir" {
				if e = dst.mkdir(to); e != nil {
					return result, e
				}
				result.Directories++
			} else {
				existing, e := dst.lstat(to)
				if e != nil || !existing.IsDir() || existing.Mode()&os.ModeSymlink != 0 {
					return result, errors.New("existing destination directory changed")
				}
			}
			continue
		}
		now, e := src.lstat(from)
		if e != nil {
			return result, e
		}
		if !sameTransferSource(item.original, now) {
			return result, errors.New("source changed after planning")
		}
		if item.action == "skip" {
			same, e := identicalTransfer(ctx, src, dst, from, to)
			if e != nil {
				return result, e
			}
			if !same {
				return result, errors.New("previously identical destination disappeared")
			}
			_, hash, e := transferSnapshot(ctx, src, from)
			if e != nil {
				return result, e
			}
			if hash != item.hash {
				return result, errors.New("skipped source changed after planning")
			}
			result.SkippedFiles++
			if options.verify {
				result.VerifiedFiles++
			}
			continue
		}
		var count int64
		if src.remote == nil {
			count, e = uploadFile(ctx, dst.remote, from, to)
		} else {
			count, e = downloadFile(ctx, src.remote, from, to)
		}
		if e != nil {
			return result, e
		}
		result.Bytes += count
		result.Files++
		if options.verify {
			if _, e = verifyTransfer(ctx, src, dst, from, to, item.hash, item.original); e != nil {
				return result, e
			}
			result.VerifiedFiles++
		}
		if options.preserveTimes {
			now, e := src.lstat(from)
			if e != nil || !sameTransferSource(item.original, now) {
				return result, errors.New("source changed before mtime preservation")
			}
			if e = preserveTransferTime(ctx, dst, to, item.original); e != nil {
				return result, e
			}
			result.TimesPreserved++
		}
		if progress != nil {
			if e = progress(result.Bytes); e != nil {
				return result, e
			}
		}
	}
	if options.preserveTimes {
		for i := len(plan) - 1; i >= 0; i-- {
			item := plan[i]
			if item.directory && item.action == "mkdir" {
				if e = preserveTransferTime(ctx, dst, dst.join(destination, item.relative), item.original); e != nil {
					return result, e
				}
				result.TimesPreserved++
			}
		}
	}
	result.Verified = options.verify
	return result, nil
}
