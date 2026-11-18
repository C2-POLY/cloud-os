package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// mapAPI serves the Photos map (#40): one PMTiles file of vector tiles from
// the maps folder (mounted read-only), read by HTTP ranges, and the account's
// geolocated photos. Nothing here calls anywhere: the map is drawn in the
// browser from the box alone.
type mapAPI struct {
	dir   string // the maps folder; "" when none is configured
	files *storage.Store
}

// tiles returns the path and name of the first .pmtiles file of the maps
// folder, by name; "" when there is none.
func (a *mapAPI) tiles() (string, string) {
	if a.dir == "" {
		return "", ""
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return "", ""
	}
	names := []string{}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".pmtiles") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", ""
	}
	sort.Strings(names)
	return filepath.Join(a.dir, names[0]), names[0]
}

// MapInfo tells the browser whether a map can be drawn.
type MapInfo struct {
	Tiles bool   `json:"tiles"`
	Name  string `json:"name,omitempty"` // the .pmtiles file used
}

func (a *mapAPI) info(w http.ResponseWriter, _ *http.Request) {
	_, name := a.tiles()
	writeJSON(w, http.StatusOK, MapInfo{Tiles: name != "", Name: name})
}

// tileFile serves the PMTiles file, by HTTP ranges (206), read only.
func (a *mapAPI) tileFile(w http.ResponseWriter, r *http.Request) {
	path, name := a.tiles()
	if path == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no map installed"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.pmtiles")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// MapPoint is a photo or video on the map.
type MapPoint struct {
	TimelineEntry
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// points lists the account's live photos and videos that have a position.
func (a *mapAPI) points(w http.ResponseWriter, r *http.Request) {
	items, err := a.files.GeoItems(r.Context(), owner(r), timeZoneOf(r))
	if err != nil {
		failFiles(w, err)
		return
	}
	out := make([]MapPoint, len(items))
	for i, it := range items {
		out[i] = MapPoint{
			TimelineEntry: TimelineEntry{Entry: entryOf(it.File), At: it.At.Format(wallClock), Taken: it.Taken, DurationMS: it.DurationMS},
			Lat:           it.Latitude,
			Lon:           it.Longitude,
		}
	}
	writeJSON(w, http.StatusOK, out)
}
