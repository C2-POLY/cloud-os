// Package smbfs serves an account's space over SMB, for the Finder and the
// Explorer. This first version is read-only: it reads the index and the files
// through storage and has no write path at all, so every create, write,
// rename, delete or change of attributes a client sends is refused with access
// denied. The SMB protocol itself is a library's (docs/decisions/006-smb.md);
// an adapter turns that library's file system interface into calls to View.
package smbfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// ShareName is the one share a signed-in account sees: its drive.
const ShareName = "drive"

// ErrReadOnly is what every write gets. It wraps os.ErrPermission, which the
// SMB libraries answer with STATUS_ACCESS_DENIED.
var ErrReadOnly = fmt.Errorf("smbfs: the drive is read-only over SMB: %w", os.ErrPermission)

// Info describes one file or folder of the view.
type Info struct {
	Name     string
	Size     int64
	IsDir    bool
	Modified time.Time
	Created  time.Time
	ID       uint64 // the index row's id; 0 for the root
}

// View is the read-only view of one account's space.
type View struct {
	Store *storage.Store
	Owner string
}

// Clean turns a path as an SMB client sends it ("dir\\file", "\\dir", "")
// into the stored form ("dir/file"; "" is the root). Anything that is not a
// plain path of the drive is reported as not found, without reaching storage:
// a named stream ("file:stream:$DATA"), a "." or ".." segment, a control
// character or NUL, invalid UTF-8, an empty segment in the middle.
func Clean(name string) (string, error) {
	p := strings.ReplaceAll(name, `\`, "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}
	if strings.ContainsRune(p, ':') {
		// Named streams (Finder tags, resource forks, Zone.Identifier) are not
		// served; a drive letter or a device name is not a path of the drive.
		return "", fmt.Errorf("smbfs: %q: %w", name, os.ErrNotExist)
	}
	clean, err := storage.CleanPath(p)
	if err != nil {
		return "", fmt.Errorf("smbfs: %q: %w", name, os.ErrNotExist)
	}
	return clean, nil
}

func infoOf(f storage.File) Info {
	return Info{
		Name:     path.Base(f.Path),
		Size:     f.Size,
		IsDir:    f.IsDir(),
		Modified: f.ModifiedAt,
		Created:  f.CreatedAt,
		ID:       uint64(f.ID),
	}
}

func rootInfo() Info { return Info{Name: "", IsDir: true, Modified: time.Unix(0, 0).UTC()} }

// toOS maps storage errors to the os errors the SMB libraries understand.
func toOS(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, storage.ErrInvalidPath):
		return fmt.Errorf("%w: %w", os.ErrNotExist, err)
	}
	return err
}

// Stat describes the file or folder at the client path name.
func (v *View) Stat(ctx context.Context, name string) (Info, error) {
	p, err := Clean(name)
	if err != nil {
		return Info{}, err
	}
	if p == "" {
		return rootInfo(), nil
	}
	f, err := v.Store.Lookup(ctx, v.Owner, p)
	if err != nil {
		return Info{}, toOS(err)
	}
	return infoOf(f), nil
}

// List returns the children of the folder at the client path name.
func (v *View) List(ctx context.Context, name string) ([]Info, error) {
	p, err := Clean(name)
	if err != nil {
		return nil, err
	}
	children, err := v.Store.List(ctx, v.Owner, p)
	if err != nil {
		return nil, toOS(err)
	}
	out := make([]Info, 0, len(children))
	for _, c := range children {
		out = append(out, infoOf(c))
	}
	return out, nil
}

// Open opens the file at the client path name for reading. A folder has no
// bytes to read: it is described by Stat and listed by List.
func (v *View) Open(ctx context.Context, name string) (*os.File, Info, error) {
	p, err := Clean(name)
	if err != nil {
		return nil, Info{}, err
	}
	if p == "" {
		return nil, Info{}, fmt.Errorf("smbfs: the root is a folder: %w", os.ErrInvalid)
	}
	f, err := v.Store.Lookup(ctx, v.Owner, p)
	if err != nil {
		return nil, Info{}, toOS(err)
	}
	if f.IsDir() {
		return nil, Info{}, fmt.Errorf("smbfs: %s is a folder: %w", p, os.ErrInvalid)
	}
	r, err := v.Store.OpenFile(f)
	if err != nil {
		return nil, Info{}, toOS(err)
	}
	return r, infoOf(f), nil
}
