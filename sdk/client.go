package sdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Default transport settings.
const (
	defaultTimeout      = 30 * time.Second
	defaultMaxRetries   = 2
	defaultRetryBackoff = 200 * time.Millisecond
	defaultUserAgent    = "oneops-go-sdk"
)

// Hooks are optional, framework-free observability callbacks. Any field may be
// nil. They must not block; the SDK invokes them synchronously.
type Hooks struct {
	// OnRequest fires before each attempt.
	OnRequest func(ctx context.Context, method, path string)
	// OnResponse fires after a response is received (any status).
	OnResponse func(ctx context.Context, method, path string, status int, dur time.Duration)
	// OnRetry fires before a retry is scheduled, with the attempt number (0-based)
	// and the error/status that triggered it.
	OnRetry func(ctx context.Context, method, path string, attempt int, err error)
}

// Config configures a Client. Only BaseURL is required.
type Config struct {
	// BaseURL is the platform root, e.g. https://oneops.internal:8080 (no trailing /v1).
	BaseURL string
	// Token is the bearer token; empty is allowed when the server has auth disabled.
	Token string
	// HTTPClient overrides the underlying client (for proxies, TLS, transport pooling).
	HTTPClient *http.Client
	// Timeout bounds each attempt (default 30s). Zero uses the default; negative disables.
	Timeout time.Duration
	// MaxRetries is the number of retries for idempotent requests (default 2).
	MaxRetries int
	// RetryBackoff is the base backoff, doubled each attempt (default 200ms).
	RetryBackoff time.Duration
	// UserAgent overrides the default User-Agent.
	UserAgent string
	// Hooks are optional observability callbacks.
	Hooks Hooks
}

// Client is the OneOps API client. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	// Governance exposes the constitutional write operations.
	Governance *GovernanceClient
	// Query exposes the read APIs.
	Query *QueryClient
	// Admin exposes the administration APIs.
	Admin *AdminClient
}

// NewClient builds a Client. It fails only on an empty/invalid BaseURL.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("oneops: BaseURL is required")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultMaxRetries
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	c := &Client{cfg: cfg, http: hc}
	c.Governance = &GovernanceClient{c: c}
	c.Query = &QueryClient{c: c}
	c.Admin = &AdminClient{c: c}
	return c, nil
}

// do executes one request with the platform's headers and a bounded retry policy
// for idempotent requests (GET, or any request carrying Idempotency-Key — the
// server dedupes those). Non-2xx responses decode to *APIError. out may be nil.
func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, body, out any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("oneops: encode request: %w", err)
		}
		bodyBytes = b
	}
	idempotent := method == http.MethodGet || headers["Idempotency-Key"] != ""

	attempts := c.cfg.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := c.attempt(ctx, method, path, headers, bodyBytes)
		if err != nil {
			lastErr = err
			if !idempotent || attempt == attempts-1 {
				return err
			}
			if c.cfg.Hooks.OnRetry != nil {
				c.cfg.Hooks.OnRetry(ctx, method, path, attempt, err)
			}
			if serr := sleepCtx(ctx, c.backoff(attempt)); serr != nil {
				return serr
			}
			continue
		}
		if isRetryableStatus(resp.StatusCode) && idempotent && attempt < attempts-1 {
			lastErr = statusOnlyError(resp.StatusCode)
			drain(resp)
			if c.cfg.Hooks.OnRetry != nil {
				c.cfg.Hooks.OnRetry(ctx, method, path, attempt, lastErr)
			}
			if serr := sleepCtx(ctx, c.backoff(attempt)); serr != nil {
				return serr
			}
			continue
		}
		return decode(resp, out)
	}
	return lastErr
}

// attempt performs a single HTTP round-trip and invokes the request/response hooks.
func (c *Client) attempt(ctx context.Context, method, path string, headers map[string]string, bodyBytes []byte) (*http.Response, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if c.cfg.Timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
	}

	var rdr io.Reader
	if bodyBytes != nil {
		rdr = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, c.cfg.BaseURL+path, rdr)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("oneops: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	reqID := headers["X-Request-ID"]
	if reqID == "" {
		reqID = newRequestID()
	}
	req.Header.Set("X-Request-ID", reqID)
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	if c.cfg.Hooks.OnRequest != nil {
		c.cfg.Hooks.OnRequest(ctx, method, path)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if cancel != nil {
		// Cancel is safe once the body is fully consumed by decode/drain; but with
		// a per-attempt timeout we must not cancel before reading the body. We only
		// reach here with the response available, so schedule cancel after read by
		// wrapping the body. Simpler: keep the timeout tied to the attempt via a
		// body-closing wrapper.
		if err != nil {
			cancel()
		} else {
			resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("oneops: request %s %s: %w", method, path, err)
	}
	if c.cfg.Hooks.OnResponse != nil {
		c.cfg.Hooks.OnResponse(ctx, method, path, resp.StatusCode, time.Since(start))
	}
	return resp, nil
}

func (c *Client) backoff(attempt int) time.Duration {
	return c.cfg.RetryBackoff * time.Duration(1<<attempt)
}

// cancelBody cancels the per-attempt context when the body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func decode(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return parseProblem(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("oneops: decode response: %w", err)
	}
	return nil
}

func parseProblem(resp *http.Response) error {
	var p struct {
		Title    string `json:"title"`
		Detail   string `json:"detail"`
		Instance string `json:"instance"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p) // body may be empty; status still classifies
	title := p.Title
	if title == "" {
		title = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Title: title, Detail: p.Detail, RequestID: p.Instance}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// statusOnlyError is the placeholder error carried between retries of a status.
func statusOnlyError(code int) error {
	return &APIError{Status: code, Title: http.StatusText(code)}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// etag renders a row version as the If-Match / ETag value the server expects.
func etag(v int64) string { return `"` + strconv.FormatInt(v, 10) + `"` }
