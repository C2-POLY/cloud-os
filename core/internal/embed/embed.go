// Package embed gets the vector of a search from the workers, and reads which
// embedding model and version they serve (docs/architecture/embeddings.md).
// The core runs no model: the worker's query endpoint
// (worker/src/worker/embedserver.py) encodes the text, on the Compose network
// only. Shared by the photo search (#71) and the document search (#70).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Timeout bounds a query to the workers: past it, the search answers 503.
// A query takes tens of milliseconds; the margin is for a worker whose model
// was paged out while it sat idle (seen at 4 s once in Docker Desktop).
const Timeout = 5 * time.Second

// ErrUnavailable is returned when no worker could encode the query: none
// configured, none reachable, too slow, or without the model.
var ErrUnavailable = errors.New("the workers cannot encode the query")

// Client asks the worker's query endpoint for vectors.
type Client struct {
	URL  string       // the worker, "http://worker:8090"; "" disables queries
	HTTP *http.Client // nil: a client with Timeout
}

// Vector is the vector of a query and the model version that made it: only
// stored vectors of the same version compare with it.
type Vector struct {
	Model   string
	Version string
	Values  []float32
}

// Query returns the vector of text for the named model.
func (c *Client) Query(ctx context.Context, model, text string) (Vector, error) {
	if c == nil || c.URL == "" {
		return Vector{}, fmt.Errorf("%w: no worker URL", ErrUnavailable)
	}
	body, err := json.Marshal(map[string]any{"model": model, "texts": []string{text}})
	if err != nil {
		return Vector{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/embed", bytes.NewReader(body))
	if err != nil {
		return Vector{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: Timeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return Vector{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return Vector{}, fmt.Errorf("%w: %s: %s", ErrUnavailable, res.Status, bytes.TrimSpace(msg))
	}
	var out struct {
		Model   string      `json:"model"`
		Version string      `json:"version"`
		Vectors [][]float32 `json:"vectors"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return Vector{}, fmt.Errorf("%w: bad answer: %v", ErrUnavailable, err)
	}
	if out.Model != model || out.Version == "" || len(out.Vectors) != 1 || len(out.Vectors[0]) == 0 {
		return Vector{}, fmt.Errorf("%w: bad answer for model %q", ErrUnavailable, model)
	}
	return Vector{Model: out.Model, Version: out.Version, Values: out.Vectors[0]}, nil
}

// Literal is the vector as pgvector's text input, "[0.1,-0.2]"; cast it with ::vector.
func (v Vector) Literal() string {
	var b strings.Builder
	b.Grow(len(v.Values) * 12)
	b.WriteByte('[')
	for i, x := range v.Values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// Due returns the version of a model the workers declared and the core has
// not queued its contents for yet (requeued_at is NULL): a new model, or a
// new version of one. ok is false when there is nothing to do.
func Due(ctx context.Context, pool *pgxpool.Pool, model string) (version string, ok bool, err error) {
	err = pool.QueryRow(ctx, `SELECT version FROM embedding_models WHERE name = $1 AND requeued_at IS NULL`, model).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return version, err == nil, err
}

// MarkRequeued records that every content was queued for this version of the
// model. A version declared since then is left due.
func MarkRequeued(ctx context.Context, pool *pgxpool.Pool, model, version string) error {
	_, err := pool.Exec(ctx, `UPDATE embedding_models SET requeued_at = now() WHERE name = $1 AND version = $2 AND requeued_at IS NULL`, model, version)
	return err
}
