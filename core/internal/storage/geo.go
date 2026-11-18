package storage

import "context"

// GeoItem is a live photo or video of the timeline with where it was taken.
type GeoItem struct {
	TimelineItem
	Latitude, Longitude float64
}

// MaxGeoItems bounds the points of one map, newest first.
const MaxGeoItems = 100_000

// GeoItems returns the owner's live photos and videos that have a position
// (media_meta, read by the workers), newest first. tz is an IANA time zone
// name the caller checked.
func (s *Store) GeoItems(ctx context.Context, owner, tz string) ([]GeoItem, error) {
	owner, err := CleanOwner(owner)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT t.*, g.latitude, g.longitude
		FROM (`+timelineBase(TimelineMedia)+`) t JOIN media_meta g ON g.sha256 = t.sha256
		WHERE g.latitude IS NOT NULL AND g.longitude IS NOT NULL
		ORDER BY t.at DESC, t.path DESC LIMIT $3`, owner, tz, MaxGeoItems)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GeoItem{}
	for rows.Next() {
		var it GeoItem
		f := &it.File
		if err := rows.Scan(&f.ID, &f.Owner, &f.Path, &f.Kind, &f.Size, &f.MIME, &f.SHA256, &f.CreatedAt, &f.ModifiedAt, &f.IndexedAt, &f.TrashID, &it.At, &it.Taken, &it.DurationMS, &it.Latitude, &it.Longitude); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
