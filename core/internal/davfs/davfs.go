// Package davfs serves a signed-in account's space over WebDAV, for the Finder
// and the Explorer. Every operation goes through storage: saving keeps the old
// content as a version, deleting goes to the trash, renaming is a journaled
// move. The Finder's own files (.DS_Store, ._*) live apart, in AppleDouble.
package davfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/net/webdav"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// FS is the webdav.FileSystem of the account in the request's context.
type FS struct {
	Store      *storage.Store
	AppleFiles *AppleDouble
}

var _ webdav.FileSystem = (*FS)(nil)

// IsAppleFile reports whether name is one of the Finder's own files, which are
// kept apart: neither indexed nor sent to the trash.
func IsAppleFile(name string) bool {
	base := path.Base(name)
	return base == ".DS_Store" || strings.HasPrefix(base, "._") || base == ".localized" || base == ".TemporaryItems"
}

func owner(ctx context.Context) (string, error) {
	a, ok := auth.AccountFrom(ctx)
	if !ok {
		return "", os.ErrPermission
	}
	return a.Name, nil
}

// clean turns a WebDAV name ("/a/b") into a storage path ("a/b"; "" is the root).
func clean(name string) string {
	return strings.Trim(path.Clean("/"+name), "/")
}

// toOS maps storage errors to the os errors the webdav handler understands.
func toOS(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound):
		return os.ErrNotExist
	case errors.Is(err, storage.ErrExists):
		return os.ErrExist
	case errors.Is(err, storage.ErrInvalidPath):
		return os.ErrInvalid
	}
	return err
}

func (d *FS) parentExists(ctx context.Context, own, p string) error {
	parent := path.Dir(p)
	if parent == "." || parent == "" {
		return nil
	}
	f, err := d.Store.Lookup(ctx, own, parent)
	if err != nil {
		return toOS(err)
	}
	if !f.IsDir() {
		return os.ErrNotExist
	}
	return nil
}

// Mkdir creates one folder; its parent must exist (WebDAV answers 409 otherwise).
func (d *FS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	own, err := owner(ctx)
	if err != nil {
		return err
	}
	p := clean(name)
	if p == "" {
		return os.ErrExist
	}
	if IsAppleFile(p) {
		return d.AppleFiles.Mkdir(own, p)
	}
	if err := d.parentExists(ctx, own, p); err != nil {
		return err
	}
	_, err = d.Store.Mkdir(ctx, own, p)
	return toOS(err)
}

// OpenFile opens a file or folder to read, or a file to write: what is written
// is stored when the file is closed, replacing an existing file (whose content
// becomes a version).
func (d *FS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	own, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	p := clean(name)
	if IsAppleFile(p) {
		return d.AppleFiles.OpenFile(own, p, flag)
	}
	writing := flag&(os.O_WRONLY|os.O_RDWR) != 0
	if writing {
		if p == "" {
			return nil, os.ErrPermission
		}
		if err := d.parentExists(ctx, own, p); err != nil {
			return nil, err
		}
		if f, err := d.Store.Lookup(ctx, own, p); err == nil {
			if f.IsDir() {
				return nil, os.ErrExist
			}
			if flag&os.O_EXCL != 0 {
				return nil, os.ErrExist
			}
		} else if !errors.Is(err, storage.ErrNotFound) {
			return nil, err
		} else if flag&os.O_CREATE == 0 {
			return nil, os.ErrNotExist
		}
		w, err := d.Store.Create(ctx, own, p, storage.PutOptions{ExpectSize: -1, Replace: true})
		if err != nil {
			return nil, toOS(err)
		}
		return &writeFile{w: w, name: path.Base(p)}, nil
	}

	if p == "" {
		return &dirFile{fs: d, ctx: ctx, owner: own, path: "", info: rootInfo()}, nil
	}
	f, err := d.Store.Lookup(ctx, own, p)
	if err != nil {
		return nil, toOS(err)
	}
	if f.IsDir() {
		return &dirFile{fs: d, ctx: ctx, owner: own, path: p, info: infoOf(f)}, nil
	}
	r, _, err := d.Store.Open(ctx, own, p)
	if err != nil {
		return nil, toOS(err)
	}
	return &readFile{File: r, info: infoOf(f)}, nil
}

// RemoveAll sends the file or folder to the trash. Nothing is erased.
func (d *FS) RemoveAll(ctx context.Context, name string) error {
	own, err := owner(ctx)
	if err != nil {
		return err
	}
	p := clean(name)
	if p == "" {
		return os.ErrPermission
	}
	if IsAppleFile(p) {
		return d.AppleFiles.Remove(own, p)
	}
	_, err = d.Store.Trash(ctx, own, p)
	return toOS(err)
}

// Rename moves a file or folder with the journaled move. The webdav handler
// trashes an existing destination first when the client asks to overwrite.
func (d *FS) Rename(ctx context.Context, oldName, newName string) error {
	own, err := owner(ctx)
	if err != nil {
		return err
	}
	from, to := clean(oldName), clean(newName)
	if from == "" || to == "" {
		return os.ErrPermission
	}
	if IsAppleFile(from) || IsAppleFile(to) {
		return d.AppleFiles.Rename(own, from, to)
	}
	if err := d.parentExists(ctx, own, to); err != nil {
		return err
	}
	_, err = d.Store.Move(ctx, own, from, to)
	return toOS(err)
}

// Stat describes a file or folder.
func (d *FS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	own, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	p := clean(name)
	if p == "" {
		return rootInfo(), nil
	}
	if IsAppleFile(p) {
		return d.AppleFiles.Stat(own, p)
	}
	f, err := d.Store.Lookup(ctx, own, p)
	if err != nil {
		return nil, toOS(err)
	}
	return infoOf(f), nil
}

// fileInfo describes an index row to the webdav handler.
type fileInfo struct {
	name string
	size int64
	dir  bool
	mod  time.Time
	mime string
	etag string
}

func infoOf(f storage.File) *fileInfo {
	fi := &fileInfo{name: path.Base(f.Path), size: f.Size, dir: f.IsDir(), mod: f.ModifiedAt, mime: f.MIME}
	if len(f.SHA256) > 0 {
		fi.etag = `"` + hexString(f.SHA256) + `"`
	}
	return fi
}

func rootInfo() *fileInfo { return &fileInfo{name: "/", dir: true, mod: time.Now()} }

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) ModTime() time.Time { return fi.mod }
func (fi *fileInfo) IsDir() bool        { return fi.dir }
func (fi *fileInfo) Sys() any           { return nil }
func (fi *fileInfo) Mode() fs.FileMode {
	if fi.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

// ContentType and ETag spare the webdav handler from reading files to guess them.
func (fi *fileInfo) ContentType(context.Context) (string, error) {
	if fi.dir || fi.mime == "" {
		return "", webdav.ErrNotImplemented
	}
	return fi.mime, nil
}

func (fi *fileInfo) ETag(context.Context) (string, error) {
	if fi.etag == "" {
		return "", webdav.ErrNotImplemented
	}
	return fi.etag, nil
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i], out[2*i+1] = digits[c>>4], digits[c&15]
	}
	return string(out)
}

// readFile is a stored file opened for reading.
type readFile struct {
	*os.File
	info *fileInfo
}

func (f *readFile) Stat() (os.FileInfo, error)         { return f.info, nil }
func (f *readFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
func (f *readFile) Write([]byte) (int, error)          { return 0, os.ErrPermission }
func (f *readFile) ReadFrom(io.Reader) (int64, error)  { return 0, os.ErrPermission }

// dirFile is a folder: it lists its children, Finder files left out.
type dirFile struct {
	fs    *FS
	ctx   context.Context
	owner string
	path  string
	info  *fileInfo
	read  bool
}

func (f *dirFile) Close() error                   { return nil }
func (f *dirFile) Read([]byte) (int, error)       { return 0, os.ErrInvalid }
func (f *dirFile) Seek(int64, int) (int64, error) { return 0, os.ErrInvalid }
func (f *dirFile) Write([]byte) (int, error)      { return 0, os.ErrPermission }
func (f *dirFile) Stat() (os.FileInfo, error)     { return f.info, nil }

func (f *dirFile) Readdir(count int) ([]os.FileInfo, error) {
	if f.read {
		if count > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	f.read = true
	children, err := f.fs.Store.List(f.ctx, f.owner, f.path)
	if err != nil {
		return nil, toOS(err)
	}
	out := make([]os.FileInfo, 0, len(children))
	for _, c := range children {
		out = append(out, infoOf(c))
	}
	return out, nil
}

// writeFile collects what a client writes and stores it on Close.
type writeFile struct {
	w    *storage.PendingFile
	name string
	size int64
	err  error
}

func (f *writeFile) Write(b []byte) (int, error) {
	n, err := f.w.Write(b)
	f.size += int64(n)
	if err != nil {
		f.err = err
	}
	return n, err
}

func (f *writeFile) Close() error {
	if f.err != nil {
		f.w.Abort()
		return f.err
	}
	_, err := f.w.Commit()
	return toOS(err)
}

func (f *writeFile) Read([]byte) (int, error) { return 0, os.ErrInvalid }
func (f *writeFile) Seek(off int64, whence int) (int64, error) {
	// The handler seeks to the end to learn the size it wrote.
	if whence == io.SeekEnd && off == 0 || whence == io.SeekCurrent && off == 0 {
		return f.size, nil
	}
	return 0, os.ErrInvalid
}
func (f *writeFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrInvalid }
func (f *writeFile) Stat() (os.FileInfo, error) {
	return &fileInfo{name: f.name, size: f.size, mod: time.Now()}, nil
}
