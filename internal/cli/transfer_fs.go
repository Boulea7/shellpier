package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/pkg/sftp"
)

type transferFile interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Stat() (os.FileInfo, error)
}
type transferFS struct{ remote *sftp.Client }

func (f transferFS) lstat(p string) (os.FileInfo, error) {
	if f.remote != nil {
		return f.remote.Lstat(p)
	}
	return os.Lstat(p)
}
func (f transferFS) join(a, b string) string {
	if f.remote != nil {
		return path.Join(a, b)
	}
	return filepath.Join(a, filepath.FromSlash(b))
}
func (f transferFS) parent(p string) string {
	if f.remote != nil {
		return path.Dir(p)
	}
	return filepath.Dir(p)
}
func (f transferFS) canonical(p string) (string, error) {
	if f.remote != nil {
		return f.remote.RealPath(p)
	}
	a, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(a)
}
func (f transferFS) destination(p string) (string, error) {
	a, e := f.canonical(f.parent(p))
	if e != nil {
		return "", e
	}
	base := filepath.Base(p)
	if f.remote != nil {
		base = path.Base(p)
	}
	return f.join(a, base), nil
}
func (f transferFS) open(p string, flags int) (transferFile, error) {
	if flags&os.O_CREATE == 0 {
		before, e := f.lstat(p)
		if e != nil {
			return nil, e
		}
		if !before.Mode().IsRegular() {
			return nil, errors.New("transfer path must be a regular file, not a symlink")
		}
	}
	if f.remote != nil {
		return f.remote.OpenFile(p, flags)
	}
	if flags == os.O_RDONLY {
		return openTransferSource(p)
	}
	file, e := os.OpenFile(p, flags, 0600)
	if e == nil {
		if e = platform.SecureFile(p); e != nil {
			file.Close()
			return nil, e
		}
	}
	return file, e
}
func (f transferFS) mkdir(p string) error {
	if f.remote != nil {
		return f.remote.Mkdir(p)
	}
	return os.Mkdir(p, 0700)
}
func (f transferFS) remove(p string) error {
	if f.remote != nil {
		return f.remote.Remove(p)
	}
	return os.Remove(p)
}
func (f transferFS) link(a, b string) error {
	if f.remote != nil {
		return f.remote.Link(a, b)
	}
	return os.Link(a, b)
}
func (f transferFS) entries(p string) ([]os.FileInfo, error) {
	if f.remote != nil {
		return f.remote.ReadDir(p)
	}
	entries, e := os.ReadDir(p)
	if e != nil {
		return nil, e
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		i, e := entry.Info()
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, nil
}

func transferDigest(ctx context.Context, f transferFS, p string) (string, int64, error) {
	file, e := f.open(p, os.O_RDONLY)
	if e != nil {
		return "", 0, e
	}
	defer file.Close()
	before, e := file.Stat()
	if e != nil {
		return "", 0, e
	}
	h := sha256.New()
	n, e := transferCopy(ctx, h, file, nil)
	if e != nil {
		return "", n, e
	}
	after, e := file.Stat()
	if e != nil {
		return "", n, e
	}
	if !sameTransferSource(before, after) || n != before.Size() {
		return "", n, errors.New("source changed while hashing")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func sameTransferSource(a, b os.FileInfo) bool {
	return a.Mode().IsRegular() && b.Mode().IsRegular() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func transferCopy(ctx context.Context, w io.Writer, r io.Reader, progress func(int64) error) (int64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if e := ctx.Err(); e != nil {
			return total, e
		}
		n, e := r.Read(buf)
		if n > 0 {
			written, we := w.Write(buf[:n])
			total += int64(written)
			if we != nil {
				return total, we
			}
			if written != n {
				return total, io.ErrShortWrite
			}
			if progress != nil {
				if pe := progress(total); pe != nil {
					return total, pe
				}
			}
		}
		if e == io.EOF {
			return total, nil
		}
		if e != nil {
			return total, e
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}
func safeTransferName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return false
	}
	return true
}
