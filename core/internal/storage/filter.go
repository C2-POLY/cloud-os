package storage

import (
	"fmt"
	"strings"
	"time"
)

// FileType narrows a search to a kind of file, by type MIME as on the
// timeline: images and videos on one side, everything else on the other.
type FileType string

const (
	AnyType    FileType = ""
	TypeDocs   FileType = "docs"   // files that are not images or videos
	TypePhotos FileType = "photos" // images and videos
)

// Filter narrows a search to a type of file and to a period. Its zero value
// keeps everything. With a type or a period, folders are left out.
type Filter struct {
	Type FileType
	// From is the first day kept and To the last, both included, on the wall
	// clock of Zone; a zero date leaves that side open. A file's day is the
	// one it has on the timeline: when the photo was taken (media_meta), else
	// when the file was last modified.
	From, To time.Time
	// Zone is the IANA time zone of the days ("Europe/Paris"); "" is UTC.
	Zone string
}

// IsZero tells the filter keeps everything.
func (f Filter) IsZero() bool {
	return f.Type == AnyType && f.From.IsZero() && f.To.IsZero()
}

// SQL returns the conditions of the filter on the files row named alias, as
// " AND …" (empty for the zero filter), and args with the values they refer
// to appended: the placeholders are numbered after the args given.
func (f Filter) SQL(alias string, args []any) (string, []any) {
	if f.IsZero() {
		return "", args
	}
	var b strings.Builder
	fmt.Fprintf(&b, ` AND %s.kind = 'file'`, alias)
	media := fmt.Sprintf(`(%[1]s.mime LIKE 'image/%%' OR %[1]s.mime LIKE 'video/%%')`, alias)
	switch f.Type {
	case TypeDocs:
		b.WriteString(` AND NOT ` + media)
	case TypePhotos:
		b.WriteString(` AND ` + media)
	}
	if f.From.IsZero() && f.To.IsZero() {
		return b.String(), args
	}
	zone := f.Zone
	if zone == "" {
		zone = "UTC"
	}
	args = append(args, zone)
	day := fmt.Sprintf(`COALESCE((SELECT m.taken_at FROM media_meta m WHERE m.sha256 = %[1]s.sha256), %[1]s.modified_at AT TIME ZONE $%[2]d)`, alias, len(args))
	if !f.From.IsZero() {
		args = append(args, f.From.Format(time.DateOnly))
		fmt.Fprintf(&b, ` AND %s >= $%d::date`, day, len(args))
	}
	if !f.To.IsZero() {
		args = append(args, f.To.Format(time.DateOnly))
		fmt.Fprintf(&b, ` AND %s < $%d::date + 1`, day, len(args))
	}
	return b.String(), args
}
