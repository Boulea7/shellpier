package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Boulea7/shellpier/internal/platform"
)

type transferSummary struct {
	Command        string             `json:"command"`
	Bytes          int64              `json:"bytes"`
	Files          int                `json:"files"`
	Directories    int                `json:"directories"`
	Warnings       []string           `json:"warnings"`
	DryRun         bool               `json:"dry_run,omitempty"`
	PlannedBytes   int64              `json:"planned_bytes,omitempty"`
	Plan           []transferPlanItem `json:"plan,omitempty"`
	SkippedFiles   int                `json:"skipped_files"`
	TimesPreserved int                `json:"times_preserved"`
	MtimePrecision string             `json:"mtime_precision,omitempty"`
}
type transferManifest struct {
	Version       int    `json:"version"`
	Owner         string `json:"owner"`
	ID            string `json:"id"`
	Direction     string `json:"direction"`
	HostKey       string `json:"host_key"`
	Source        string `json:"source"`
	Destination   string `json:"destination"`
	Partial       string `json:"partial"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	SourceModTime string `json:"source_mod_time"`
}

func resumeStatePath(explicit string, m transferManifest) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	cache, e := os.UserCacheDir()
	if e != nil {
		return "", e
	}
	dir := filepath.Join(cache, "shellpier-transfers")
	if e = prepareResumeCacheDir(dir, platform.SecureDir); e != nil {
		return "", e
	}
	key := sha256.Sum256([]byte(m.Direction + "\x00" + m.HostKey + "\x00" + m.Source + "\x00" + m.Destination))
	return filepath.Join(dir, hex.EncodeToString(key[:])+".json"), nil
}

func prepareResumeCacheDir(dir string, secure func(string) error) error {
	err := os.Mkdir(dir, 0700)
	if err == nil {
		if err = secure(dir); err != nil {
			return err
		}
	} else if !os.IsExist(err) {
		return err
	}
	return checkPrivateSessionDir(dir)
}
func manifestMatches(a, b transferManifest) bool {
	return a.Version == 1 && a.Owner == "ShellPier-transfer-v1" && a.Direction == b.Direction && a.HostKey == b.HostKey && a.Source == b.Source && a.Destination == b.Destination && a.SHA256 == b.SHA256 && a.Size == b.Size && a.SourceModTime == b.SourceModTime && len(a.ID) == 64 && !strings.ContainsAny(a.ID, "/\\")
}
func removeOwnedManifest(p string, owned os.FileInfo) {
	if now, e := os.Lstat(p); e == nil && os.SameFile(now, owned) {
		_ = os.Remove(p)
	}
}

func resumeTransfer(ctx context.Context, src, dst transferFS, source, destination, state, direction, hostkey string, progress func(int64) error) (result transferSummary, err error) {
	sourceInfo, e := src.lstat(source)
	if e != nil {
		return result, e
	}
	if !sourceInfo.Mode().IsRegular() {
		return result, errors.New("resume source must be a regular file, not a symlink")
	}
	source, e = src.canonical(source)
	if e != nil {
		return result, e
	}
	destination, e = dst.destination(destination)
	if e != nil {
		return result, e
	}
	digest, size, e := transferDigest(ctx, src, source)
	if e != nil {
		return result, e
	}
	afterHash, e := src.lstat(source)
	if e != nil {
		return result, e
	}
	if !sameTransferSource(sourceInfo, afterHash) {
		return result, errors.New("source identity changed while preparing resume")
	}
	expected := transferManifest{Version: 1, Owner: "ShellPier-transfer-v1", Direction: direction, HostKey: hostkey, Source: source, Destination: destination, SHA256: digest, Size: size, SourceModTime: sourceInfo.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	state, e = resumeStatePath(state, expected)
	if e != nil {
		return result, e
	}
	lock, e := os.OpenFile(state+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return result, fmt.Errorf("resume state is busy or its lock requires inspection: %w", e)
	}
	lockInfo, e := lock.Stat()
	lock.Close()
	if e != nil {
		return result, e
	}
	defer removeOwnedManifest(state+".lock", lockInfo)
	if _, e = dst.lstat(destination); !os.IsNotExist(e) {
		if e == nil {
			return result, errors.New("final destination exists; inspect it before resuming; never overwritten")
		}
		return result, e
	}
	m := expected
	if _, e = os.Lstat(state); e == nil {
		if e = checkPrivateSessionFile(state); e != nil {
			return result, e
		}
		data, e := readRegular(state, 16384)
		if e != nil {
			return result, e
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&m); e != nil {
			return result, e
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || !manifestMatches(m, expected) {
			return result, errors.New("resume manifest or source identity/content changed")
		}
		if m.Partial != dst.join(dst.parent(destination), ".shellpier-"+m.ID+".part") {
			return result, errors.New("invalid owned partial path")
		}
	} else if os.IsNotExist(e) {
		m.ID, e = secret()
		if e != nil {
			return result, e
		}
		m.Partial = dst.join(dst.parent(destination), ".shellpier-"+m.ID+".part")
		part, e := dst.open(m.Partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if e != nil {
			return result, e
		}
		if e = part.Close(); e != nil {
			return result, e
		}
		if e = writeJSONPrivate(state, m); e != nil {
			return result, fmt.Errorf("partial retained at %s; cannot record resume state: %w", m.Partial, e)
		}
	} else {
		return result, e
	}
	stateInfo, e := os.Lstat(state)
	if e != nil {
		return result, e
	}
	// The journal owns a random path. Content must still match the source prefix;
	// SFTP v3 has no portable inode identity or protection from same-account races.
	partial, e := dst.open(m.Partial, os.O_RDONLY)
	if e != nil {
		return result, e
	}
	partInfo, e := partial.Stat()
	if e != nil {
		partial.Close()
		return result, e
	}
	offset := partInfo.Size()
	if offset > size {
		partial.Close()
		return result, errors.New("partial exceeds source size")
	}
	input, e := src.open(source, os.O_RDONLY)
	if e != nil {
		partial.Close()
		return result, e
	}
	defer input.Close()
	prefix := sha256.New()
	_, e = transferCopy(ctx, prefix, partial, nil)
	partial.Close()
	if e != nil {
		return result, e
	}
	sourcePrefix := sha256.New()
	if _, e = transferCopy(ctx, sourcePrefix, io.LimitReader(input, offset), nil); e != nil {
		return result, e
	}
	if !bytes.Equal(prefix.Sum(nil), sourcePrefix.Sum(nil)) {
		return result, errors.New("partial content changed; refusing to append")
	}
	output, e := dst.open(m.Partial, os.O_WRONLY)
	if e != nil {
		return result, e
	}
	opened, e := output.Stat()
	if e != nil || !sameTransferSource(partInfo, opened) {
		output.Close()
		return result, errors.New("partial changed while opening")
	}
	if _, e = output.Seek(offset, io.SeekStart); e != nil {
		output.Close()
		return result, e
	}
	_, e = transferCopy(ctx, output, input, func(n int64) error {
		if progress != nil {
			return progress(offset + n)
		}
		return nil
	})
	closeErr := output.Close()
	if e = errors.Join(e, closeErr); e != nil {
		return result, fmt.Errorf("owned partial retained; resume with state %s: %w", state, e)
	}
	actual, n, e := transferDigest(ctx, dst, m.Partial)
	if e != nil {
		return result, e
	}
	if actual != digest || n != size {
		return result, errors.New("completed partial content differs from source")
	}
	current, _, e := transferDigest(ctx, src, source)
	if e != nil {
		return result, e
	}
	if current != digest {
		return result, errors.New("source changed during transfer")
	}
	if e = dst.link(m.Partial, destination); e != nil {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		final, e := dst.open(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if e != nil {
			return result, fmt.Errorf("cannot publish without overwriting; inspect existing final and retained partial: %w", e)
		}
		part, e := dst.open(m.Partial, os.O_RDONLY)
		if e != nil {
			final.Close()
			return result, e
		}
		_, copyErr := transferCopy(ctx, final, part, nil)
		e = errors.Join(copyErr, part.Close(), final.Close())
		if e != nil {
			return result, fmt.Errorf("non-atomic final copy incomplete; inspect final before resuming; partial/state retained: %w", e)
		}
		result.Warnings = append(result.Warnings, "hardlink unavailable; final created exclusively by non-atomic copy (FAT/SFTP compatible)")
	}
	finalDigest, finalSize, e := transferDigest(ctx, dst, destination)
	if e != nil {
		return result, fmt.Errorf("final publication could not be verified; state and partial retained: %w", e)
	}
	if finalDigest != digest || finalSize != size {
		return result, errors.New("final content changed; state and partial retained for inspection")
	}
	// Delete only a still-recognizable owned partial, never an arbitrary user path.
	if sum, n, e := transferDigest(ctx, dst, m.Partial); e == nil && sum == digest && n == size {
		if e = dst.remove(m.Partial); e != nil {
			result.Warnings = append(result.Warnings, "published successfully; owned partial cleanup failed")
		}
	} else {
		result.Warnings = append(result.Warnings, "published successfully; partial changed or could not be checked, retained")
	}
	removeOwnedManifest(state, stateInfo)
	result.Bytes = size
	result.Files = 1
	return result, nil
}
