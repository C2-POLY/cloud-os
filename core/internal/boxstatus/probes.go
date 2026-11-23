package boxstatus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/C2-POLY/cloud-os/core/internal/storage"
)

// HTTPProbe asks a service's health endpoint on the Compose network: the
// worker's GET /health, the tailscale service's GET /healthz. It answers
// when the endpoint says 2xx.
type HTTPProbe struct {
	URL    string
	Client *http.Client // nil: a client with ProbeTimeout that follows no redirect
}

// Check gets URL; the body is read (a little) and dropped.
func (p HTTPProbe) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	client := p.Client
	if client == nil {
		client = &http.Client{
			Timeout:       ProbeTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("%s: %s", p.URL, res.Status)
	}
	return nil
}

// TCPProbe answers when a TCP connection to Addr opens: the smb service, on
// smb:445. It sends nothing and closes at once.
type TCPProbe struct {
	Addr string
}

// Check opens a connection to Addr and closes it.
func (p TCPProbe) Check(ctx context.Context) error {
	if p.Addr == "" {
		return errors.New("no address")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// DataFiles is the data disk of storage's Store and its rescue folder.
type DataFiles struct {
	Store *storage.Store
}

// DiskUsage measures the disk the data root is on.
func (f DataFiles) DiskUsage() (total, free uint64, err error) {
	return storage.DiskUsage(f.Store.Root)
}

// RescuedCount is how many SMB writes wait in .cloudos/rescue.
func (f DataFiles) RescuedCount() (int, error) {
	return f.Store.RescuedCount()
}
