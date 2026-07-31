//go:build loadtest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// apiClient is a thin authenticated HTTP client for the target instance.
type apiClient struct {
	http    *http.Client
	baseURL string
	token   string
}

// do issues a request and returns its status code. The body is drained and
// discarded (not decoded) — the harness measures the API's serving cost, not
// its own JSON allocation cost, and draining is required to let the
// connection be reused from the pool.
func (c *apiClient) do(ctx context.Context, method, path string, body any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// createArtifactBody mirrors internal/httpapi/dto.go: createRequest — the
// fields required to pass ConfigObject.ValidateInception (see
// internal/httpapi/handlers_test.go: validCreate).
type createArtifactBody struct {
	Artifact       string            `json:"artifact"`
	Version        string            `json:"version"`
	Role           string            `json:"role"`
	Lifecycle      string            `json:"lifecycle"`
	RetentionClass string            `json:"retention_class"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type createArtifactResponse struct {
	CfgID string `json:"cfg_id"`
}

// createArtifact creates one artifact and returns its cfg_id. name must be
// globally unique for the run: Create enforces a unique (artifact, version)
// pair (internal/httpapi/handlers_test.go: fakeRepo.Create mirrors the real
// store's constraint).
func createArtifact(ctx context.Context, c *apiClient, name string) (string, int, error) {
	body := createArtifactBody{
		Artifact: name, Version: "1.0.0",
		Role: "governance", Lifecycle: "draft", RetentionClass: "working_material",
		Metadata: map[string]string{"source": "loadtest"},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/artifacts", bytes.NewReader(b))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode, fmt.Errorf("create artifact %s: status %d: %s", name, resp.StatusCode, string(data))
	}
	var out createArtifactResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", resp.StatusCode, err
	}
	return out.CfgID, resp.StatusCode, nil
}

// seedPool holds the artifact ids the load phase reads back. Populated once,
// before any worker starts, and never mutated again — concurrent reads of a
// frozen slice need no lock.
type seedPool struct {
	ids []string
}

func (p *seedPool) randomID(n uint64) string {
	if len(p.ids) == 0 {
		// No seed succeeded; every id-scoped read 404s, which is still a real,
		// countable response — just not a representative one. seedArtifacts
		// fails loudly precisely so this path is not silently exercised.
		return "00000000-0000-0000-0000-000000000000"
	}
	return p.ids[n%uint64(len(p.ids))]
}

// seedArtifacts creates n unique artifacts up front so the read mix (get,
// governance state, dependency traversal) exercises real rows instead of
// uniformly hitting 404s.
func seedArtifacts(ctx context.Context, c *apiClient, n int) (*seedPool, error) {
	pool := &seedPool{ids: make([]string, 0, n)}
	stamp := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("loadtest-seed-%d-%d", stamp, i)
		id, _, err := createArtifact(ctx, c, name)
		if err != nil {
			return nil, err
		}
		pool.ids = append(pool.ids, id)
	}
	return pool, nil
}
