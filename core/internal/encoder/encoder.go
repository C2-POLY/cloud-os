// Package encoder asks the workers for the vector of a search query
// (worker/src/worker/encoder.py): the core has no model of its own, and the
// worker already holds them in memory. It answers on the Compose network
// only, so nothing leaves the box. Text search (#70) uses it; the search of
// photos by their content (#71) asks it for another model.
package encoder

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
)

// DefaultURL is the worker's encoder in the Compose stack (CLOUDOS_ENCODER_URL).
const DefaultURL = "http://worker:8090"

// Timeout bounds one query, the model's first load included.
const Timeout = 5 * time.Second

// ErrUnavailable is any failure to get a vector: the worker is down or
// still starting, it does not serve the model, or it answered nonsense.
var ErrUnavailable = errors.New("the query encoder is unavailable")

// Vector is a query encoded by a model: unit length, so that the cosine
// distance ranks what it is compared with.
type Vector struct {
	Model   string    `json:"model"`
	Version string    `json:"version"`
	Values  []float32 `json:"vector"`
}

// Literal is the vector as pgvector reads it, "[0.1,-0.2]": a query passes
// it as text and casts it ($1::halfvec).
func (v Vector) Literal() string {
	var b strings.Builder
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

// Client calls the worker's POST /encode.
type Client struct {
	URL  string       // DefaultURL when empty
	HTTP *http.Client // one with Timeout when nil
}

var defaultHTTP = &http.Client{Timeout: Timeout}

// Encode returns the vector of text for model. Every failure wraps
// ErrUnavailable; the text itself is never part of an error.
func (c *Client) Encode(ctx context.Context, model, text string) (Vector, error) {
	url, hc := c.URL, c.HTTP
	if url == "" {
		url = DefaultURL
	}
	if hc == nil {
		hc = defaultHTTP
	}
	body, err := json.Marshal(map[string]string{"model": model, "text": text})
	if err != nil {
		return Vector{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/encode", bytes.NewReader(body))
	if err != nil {
		return Vector{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return Vector{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Vector{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if res.StatusCode != http.StatusOK {
		return Vector{}, fmt.Errorf("%w: status %d", ErrUnavailable, res.StatusCode)
	}
	var v Vector
	if err := json.Unmarshal(data, &v); err != nil {
		return Vector{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if v.Model != model || len(v.Values) == 0 {
		return Vector{}, fmt.Errorf("%w: answered model %q with %d values", ErrUnavailable, v.Model, len(v.Values))
	}
	return v, nil
}
