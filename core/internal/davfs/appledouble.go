package davfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/webdav"
)

// AppleDouble keeps the Finder's own files (.DS_Store, ._* resource forks)
// apart from the drive, under <data>/.cloudos/appledouble/<owner>/<path>.
// They are not the user's documents: never indexed, never trashed, and erased
// directly when the Finder deletes them.
type AppleDouble struct {
	Root string // the data root
}

func (a *AppleDouble) base(owner string) string {
	return filepath.Join(a.Root, ".cloudos", "appledouble", owner)
}

// local maps owner/p to its place under the AppleDouble folder, refusing any
// path that would leave it.
func (a *AppleDouble) local(owner, p string) (string, error) {
	if owner == "" || strings.ContainsAny(owner, `/\.`) {
		return "", os.ErrPermission
	}
	full := filepath.Join(a.base(owner), filepath.FromSlash(p))
	rel, err := filepath.Rel(a.base(owner), full)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", os.ErrPermission
	}
	return full, nil
}

func (a *AppleDouble) Mkdir(owner, p string) error {
	full, err := a.local(owner, p)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0o750)
}

func (a *AppleDouble) OpenFile(owner, p string, flag int) (webdav.File, error) {
	full, err := a.local(owner, p)
	if err != nil {
		return nil, err
	}
	if flag&os.O_CREATE != 0 {
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(full, flag, 0o600)
}

func (a *AppleDouble) Stat(owner, p string) (os.FileInfo, error) {
	full, err := a.local(owner, p)
	if err != nil {
		return nil, err
	}
	return os.Stat(full)
}

func (a *AppleDouble) Rename(owner, from, to string) error {
	src, err := a.local(owner, from)
	if err != nil {
		return err
	}
	dst, err := a.local(owner, to)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// Remove erases one of the Finder's files. It only ever acts under
// .cloudos/appledouble/<owner>; a user document cannot be reached from here.
func (a *AppleDouble) Remove(owner, p string) error {
	full, err := a.local(owner, p)
	if err != nil {
		return err
	}
	if !IsAppleFile(p) {
		return fmt.Errorf("refusing to erase %s: not a Finder file", p)
	}
	return removeAppleFile(full)
}

// removeAppleFile erases a Finder file already resolved under
// .cloudos/appledouble/<owner> by local.
func removeAppleFile(full string) error {
	if err := os.RemoveAll(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
