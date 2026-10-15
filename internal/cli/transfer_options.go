package cli

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

type transferOptions struct {
	dryRun, preserveTimes, skipExisting bool
	budget                              *transferBudget
}
type transferOptionsKey struct{}

func transferSettings(ctx context.Context) transferOptions {
	o, _ := ctx.Value(transferOptionsKey{}).(transferOptions)
	return o
}

// Every copied byte, including digest and resume validation reads, shares this
// operation's budget. Metadata requests and SSH framing are not byte-accounted.
type transferBudget struct {
	mu   sync.Mutex
	rate int64
	next time.Time
}

func (b *transferBudget) wait(ctx context.Context, n int) error {
	if b == nil || b.rate == 0 {
		return ctx.Err()
	}
	// Split time arithmetic to avoid overflow for very large user rates.
	duration := time.Duration(float64(n) / float64(b.rate) * float64(time.Second))
	b.mu.Lock()
	start := time.Now()
	if b.next.After(start) {
		start = b.next
	}
	b.next = start.Add(duration)
	until := b.next
	b.mu.Unlock()
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type transferPlanItem struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Bytes  int64  `json:"bytes"`
}

func preserveTransferTime(ctx context.Context, fs transferFS, p string, original os.FileInfo) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	current, e := fs.lstat(p)
	if e != nil {
		return e
	}
	if current.Mode()&os.ModeSymlink != 0 || !(current.Mode().IsRegular() || current.IsDir()) {
		return errors.New("refusing metadata change on symlink or special path")
	}
	// SFTP v3 timestamps are seconds. Use the same explicit precision locally.
	modified := original.ModTime().Truncate(time.Second)
	if fs.remote != nil {
		return fs.remote.Chtimes(p, modified, modified)
	}
	return os.Chtimes(p, modified, modified)
}
func existingTransfer(dst transferFS, destination string) (bool, error) {
	info, e := dst.lstat(destination)
	if os.IsNotExist(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("existing destination is not a regular file")
	}
	return true, nil
}

func transferParentDirectory(fs transferFS, p string) error {
	info, e := fs.lstat(fs.parent(p))
	if e != nil {
		return e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("destination parent must be a real directory")
	}
	return nil
}
func runSingleTransfer(ctx context.Context, src, dst transferFS, source, destination string, progress func(int64) error) (result transferSummary, err error) {
	options := transferSettings(ctx)
	info, e := src.lstat(source)
	if e != nil {
		return result, e
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("source must be a regular file, not a symlink")
	}
	if e = transferParentDirectory(dst, destination); e != nil {
		return result, e
	}
	action := "copy"
	if options.skipExisting {
		same, e := existingTransfer(dst, destination)
		if e != nil {
			return result, e
		}
		if same {
			action = "skip"
		}
	} else {
		if _, e := dst.lstat(destination); e == nil {
			return result, errors.New("destination already exists")
		} else if !os.IsNotExist(e) {
			return result, e
		}
	}
	if options.dryRun {
		result.DryRun = true
		result.Plan = []transferPlanItem{{Path: destination, Kind: "file", Action: action, Bytes: info.Size()}}
		if action == "copy" {
			result.PlannedBytes = info.Size()
		} else {
			result.SkippedFiles = 1
		}
		return result, nil
	}
	if action == "skip" {
		result.SkippedFiles = 1
		return result, nil
	}
	copyCtx := context.WithValue(ctx, transferProgressKey{}, progress)
	if src.remote == nil {
		result.Bytes, e = uploadFile(copyCtx, dst.remote, source, destination)
	} else {
		result.Bytes, e = downloadFile(copyCtx, src.remote, source, destination)
	}
	if e != nil {
		return result, e
	}
	result.Files = 1
	if options.preserveTimes {
		now, e := src.lstat(source)
		if e != nil {
			return result, e
		}
		if !sameTransferSource(info, now) {
			return result, errors.New("source changed before mtime preservation")
		}
		if e = preserveTransferTime(ctx, dst, destination, info); e != nil {
			return result, e
		}
		result.TimesPreserved = 1
	}
	return result, nil
}
