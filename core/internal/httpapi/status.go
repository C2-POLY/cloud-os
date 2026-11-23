package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/C2-POLY/cloud-os/core/internal/boxstatus"
)

// statusTimeout bounds GET /api/box/status: the probes take ProbeTimeout at
// most, in parallel, and the database reads are small.
const statusTimeout = 5 * time.Second

// statusAPI tells the box's administrators how it is doing (#102,
// docs/architecture/status.md): disks, services, backup, SMB writes kept,
// and the alerts. It only reads.
type statusAPI struct {
	collector *boxstatus.Collector
}

func (a *statusAPI) get(w http.ResponseWriter, r *http.Request) {
	if a.collector == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the box status is unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
	defer cancel()
	st, err := a.collector.Collect(ctx)
	if err != nil {
		slog.Warn("box status", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the box status is unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}
