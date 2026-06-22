// Package promquery is a minimal read-only Prometheus HTTP API client. The
// management-api uses it for the per-tenant usage rollup (#92): it issues instant
// queries and folds the vector result into a tenant_id → value map. It is
// deliberately tiny — no push, no range queries, no auth — because the only
// caller runs against the in-cluster Prometheus over the private network.
package promquery

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client queries a Prometheus server's instant-query endpoint.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the given Prometheus base URL (e.g.
// "http://prometheus:9090"). A nil httpClient gets a 10s-timeout default.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: baseURL, http: httpClient}
}

// promResponse is the subset of the Prometheus query API envelope we read.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// value is [ <unix_ts float>, "<sample as string>" ].
			Value []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

// QueryByLabel runs an instant query and returns a map keyed by the value of
// label on each result series → the series' scalar sample. Series missing the
// label, or whose sample is NaN/unparseable, are skipped. An empty/zero result
// is not an error (returns an empty map). Use this for rollups like
// `sum by (tenant_id)(increase(metric[24h]))` with label="tenant_id".
func (c *Client) QueryByLabel(ctx context.Context, query, label string) (map[string]float64, error) {
	u := c.baseURL + "/api/v1/query?" + url.Values{"query": {query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query prometheus: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned %d", resp.StatusCode)
	}

	var pr promResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("decode prometheus response: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed: %s: %s", pr.ErrorType, pr.Error)
	}

	out := make(map[string]float64, len(pr.Data.Result))
	for _, series := range pr.Data.Result {
		key, ok := series.Metric[label]
		if !ok || key == "" || len(series.Value) != 2 {
			continue
		}
		var sample string
		if err := json.Unmarshal(series.Value[1], &sample); err != nil {
			continue
		}
		v, err := strconv.ParseFloat(sample, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue // "NaN"/"+Inf" parse to non-finite floats — drop them
		}
		out[key] = v
	}
	return out, nil
}
