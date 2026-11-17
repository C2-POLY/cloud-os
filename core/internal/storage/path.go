package storage

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ErrInvalidPath is returned for an owner or a path that cannot be stored safely.
var ErrInvalidPath = errors.New("invalid path")

var ownerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// maxSegment is the longest file or folder name, in bytes, that common file systems accept.
const maxSegment = 255

// CleanOwner checks an owner name. Owners are folder names directly under the
// data root, so they are kept to a safe, lower-case alphabet.
func CleanOwner(owner string) (string, error) {
	if !ownerRe.MatchString(owner) {
		return "", fmt.Errorf("%w: owner %q must match %s", ErrInvalidPath, owner, ownerRe)
	}
	return owner, nil
}

// CleanPath turns a user path into the stored form: NFC, relative,
// '/'-separated, with no empty, '.' or '..' segment and no control character
// or backslash. It never resolves anything on disk.
func CleanPath(p string) (string, error) {
	if !utf8.ValidString(p) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalidPath)
	}
	p = norm.NFC.String(p)
	if p == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidPath)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrInvalidPath, p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return "", fmt.Errorf("%w: %q has an empty segment", ErrInvalidPath, p)
		case seg == "." || seg == "..":
			return "", fmt.Errorf("%w: %q has a %q segment", ErrInvalidPath, p, seg)
		case len(seg) > maxSegment:
			return "", fmt.Errorf("%w: a name in %q is longer than %d bytes", ErrInvalidPath, p, maxSegment)
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f || r == '\\' {
				return "", fmt.Errorf("%w: %q has a control character or a backslash", ErrInvalidPath, p)
			}
		}
	}
	return p, nil
}

// ancestors returns the folders above p, nearest last: "a/b/c" gives "a", "a/b".
func ancestors(p string) []string {
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}
