package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nodx-ops/nodx-agent/internal/collect"
)

// ErrAuthFatal marks 401/403: the claim or secret is wrong. This is an
// operator error — the caller must log.Fatal, never loop or spool.
var ErrAuthFatal = errors.New("deliver: server rejected credentials (401/403) -- fix NODX_SERVER/NODX_AGENT_SECRET/claim, never retried")

var (
	varSent      = expvar.NewInt("deliver_sent_total")
	varDropped   = expvar.NewInt("deliver_dropped_total")
	varRetryable = expvar.NewInt("deliver_retryable_total")
)

// Client posts batches to POST /api/v1/telemetry with a single reused
// http.Client (one connection pool for the process; never per-request).
type Client struct {
	base      string // server URL, trailing slash trimmed
	secret    string
	userAgent string
	http      *http.Client
}

// NewClient builds a delivery client sharing one HTTP transport.
func NewClient(serverURL, secret, version string, timeout time.Duration) *Client {
	return &Client{
		base:      strings.TrimRight(serverURL, "/"),
		secret:    secret,
		userAgent: "github.com/nodx-ops/nodx-agent/" + version,
		http:      &http.Client{Timeout: timeout},
	}
}

// Send posts one self-capped batch. nil means accepted (2xx) or permanently
// dropped (4xx non-429, counted). A non-nil non-fatal error means
// retryable (429/5xx/timeout/network): the caller spools + backs off.
// ErrAuthFatal (401/403) must terminate the process via log.Fatal.
func (c *Client) Send(ctx context.Context, batch []collect.Sample) error {
	if len(batch) == 0 {
		return nil
	}
	// Ingestion accepts ONE telemetry object per POST (array payloads 400).
	// Iterate the batch: per-sample verdicts preserve fail-open semantics
	// (a poison sample drops alone instead of taking the batch with it).
	var firstErr error
	for _, s := range batch {
		if err := c.sendOne(ctx, s); err != nil {
			if errors.Is(err, ErrAuthFatal) {
				return err
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (c *Client) sendOne(ctx context.Context, s collect.Sample) error {
	raw, err := json.Marshal(s)
	if err != nil {
		// Samples are validated at creation; unmarshalable here is a
		// programming bug: drop + counter, no retry, no spool.
		varDropped.Add(1)
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/telemetry", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("deliver: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		varRetryable.Add(1)
		return fmt.Errorf("deliver: post %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	// Drain for keep-alive reuse; bodies are small status JSON.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		varSent.Add(1)
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrAuthFatal
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		varRetryable.Add(1)
		return fmt.Errorf("deliver: retryable status %d", resp.StatusCode)
	default:
		// Other 4xx: this sample is malformed for this server; retrying or
		// spooling would poison the queue. Drop + counter.
		varDropped.Add(1)
		return nil
	}
}

// CapBatch splits samples into chunks of at most maxSamples / maxBytes wire
// bytes. The edge imposes no size limit; this self-cap keeps a single POST
// small and a single rejection cheap. Samples are indivisible: one oversized
// sample still ships alone.
func CapBatch(batch []collect.Sample, maxSamples, maxBytes int) [][]collect.Sample {
	if maxSamples < 1 {
		maxSamples = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	var chunks [][]collect.Sample
	var cur []collect.Sample
	curBytes := 2 // '[' + ']'
	for _, s := range batch {
		raw, err := json.Marshal(s)
		if err != nil {
			varDropped.Add(1)
			continue
		}
		size := len(raw) + 1 // +1 for ',' separator
		if len(cur) > 0 && (len(cur) >= maxSamples || curBytes+size > maxBytes) {
			chunks = append(chunks, cur)
			cur = nil
			curBytes = 2
		}
		cur = append(cur, s)
		curBytes += size
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}
