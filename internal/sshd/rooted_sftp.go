package sshd

import (
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

// rootedSFTP uses a host-owned directory handle, never a checked path prefix.
type rootedSFTP struct {
	root     *os.Root
	readOnly bool
}

func rootedPath(name string) string {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "."
	}
	return filepath.FromSlash(name)
}
func rootedHandlers(root *os.Root, readOnly bool) sftp.Handlers {
	h := &rootedSFTP{root: root, readOnly: readOnly}
	return sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h}
}

type rootedFile struct {
	*os.File
	appendMode bool
}

func (f *rootedFile) WriteAt(p []byte, offset int64) (int, error) {
	if f.appendMode {
		return f.File.Write(p)
	}
	return f.File.WriteAt(p, offset)
}

func (h *rootedSFTP) open(r *sftp.Request) (*rootedFile, error) {
	flags := r.Pflags()
	if h.readOnly && r.Flags != 1 {
		return nil, os.ErrPermission
	}
	mode := os.O_RDONLY
	if flags.Read && flags.Write {
		mode = os.O_RDWR
	} else if flags.Write {
		mode = os.O_WRONLY
	}
	if flags.Creat {
		mode |= os.O_CREATE
	}

	if flags.Excl {
		mode |= os.O_EXCL
	}
	// Append writes ignore the wire offset and use the kernel's append semantics.
	if flags.Append {
		mode |= os.O_APPEND
	}
	file, err := rootedOpen(h.root, rootedPath(r.Filepath), mode, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, os.ErrInvalid
	}
	if flags.Trunc {
		if err := file.Truncate(0); err != nil {
			file.Close()
			return nil, err
		}
	}
	return &rootedFile{File: file, appendMode: flags.Append}, nil
}
func (h *rootedSFTP) Fileread(r *sftp.Request) (io.ReaderAt, error)           { return h.open(r) }
func (h *rootedSFTP) Filewrite(r *sftp.Request) (io.WriterAt, error)          { return h.open(r) }
func (h *rootedSFTP) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) { return h.open(r) }
func (h *rootedSFTP) Filecmd(r *sftp.Request) error {
	if h.readOnly {
		return os.ErrPermission
	}
	name, target := rootedPath(r.Filepath), rootedPath(r.Target)
	switch r.Method {
	case "Setstat":
		flags, attrs := r.AttrFlags(), r.Attributes()
		if flags.Size && attrs.Size > math.MaxInt64 {
			return os.ErrInvalid
		}
		if flags.UidGid {
			return sftp.ErrSSHFxOpUnsupported
		}
		mode := os.O_RDONLY
		if flags.Size {
			mode = os.O_WRONLY
		}
		file, err := rootedOpen(h.root, name, mode, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return os.ErrInvalid
		}
		if flags.Size {
			if err := file.Truncate(int64(attrs.Size)); err != nil {
				return err
			}
		}
		if flags.Permissions {
			if err := file.Chmod(os.FileMode(attrs.Mode) & 0777); err != nil {
				return err
			}
		}
		if flags.Acmodtime {
			if err := rootedFileTimes(file, time.Unix(int64(attrs.Atime), 0), time.Unix(int64(attrs.Mtime), 0)); err != nil {
				return err
			}
		}
		return nil

	case "Rename", "PosixRename":
		return h.root.Rename(name, target)
	case "Mkdir":
		return h.root.Mkdir(name, 0700)
	case "Remove", "Rmdir":
		return h.root.Remove(name)
	case "Link":
		return h.root.Link(name, target)
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}
func (h *rootedSFTP) PosixRename(r *sftp.Request) error { return h.Filecmd(r) }
func (h *rootedSFTP) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		f, err := rootedOpen(h.root, rootedPath(r.Filepath), os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.IsDir() {
			f.Close()
			if err != nil {
				return nil, err
			}
			return nil, os.ErrInvalid
		}
		return &rootedDirectory{file: f}, nil
	case "Stat":
		info, err := h.root.Stat(rootedPath(r.Filepath))
		if err != nil {
			return nil, err
		}
		return rootedInfo{info}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}
func (h *rootedSFTP) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	info, err := h.root.Lstat(rootedPath(r.Filepath))
	if err != nil {
		return nil, err
	}
	return rootedInfo{info}, nil
}
func (h *rootedSFTP) RealPath(name string) (string, error) { return path.Join("/", name), nil }
func (h *rootedSFTP) Readlink(name string) (string, error) { return h.root.Readlink(rootedPath(name)) }

type rootedInfo []os.FileInfo

func (i rootedInfo) ListAt(p []os.FileInfo, offset int64) (int, error) {
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	if offset >= int64(len(i)) {
		return 0, io.EOF
	}
	n := copy(p, i[offset:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type rootedDirectory struct{ file *os.File }

// RequestServer owns the sequential READDIR cursor; SFTP has no directory seek.
func (d *rootedDirectory) ListAt(p []os.FileInfo, _ int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	infos, err := d.file.Readdir(len(p))
	return copy(p, infos), err
}
func (d *rootedDirectory) Close() error { return d.file.Close() }
