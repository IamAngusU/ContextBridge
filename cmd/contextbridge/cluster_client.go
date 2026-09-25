package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

// clusterAPIClient is the typed, content-bounded client core shared by
// interactive frontends. It carries no implicit admin fallback: its caller
// must resolve and pass the exact credential intended for the action surface.
type clusterAPIClient struct {
	baseURL string
	token   string
}

type clusterAPIError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *clusterAPIError) Error() string {
	return fmt.Sprintf("relay returned %s: %s", e.Status, e.Body)
}

func newClusterAPIClient(baseURL, token string) *clusterAPIClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &clusterAPIClient{baseURL: baseURL, token: strings.TrimSpace(token)}
}

func (c *clusterAPIClient) Submit(ctx context.Context, input cluster.SubmitRequest, idempotencyKey string) (cluster.Job, error) {
	var job cluster.Job
	headers := make(http.Header)
	if strings.TrimSpace(idempotencyKey) != "" {
		headers.Set("Idempotency-Key", strings.TrimSpace(idempotencyKey))
	}
	err := c.doJSON(ctx, http.MethodPost, "/v1/cluster/jobs?compact=1", input, &job, headers)
	return job, err
}

func (c *clusterAPIClient) Jobs(ctx context.Context, limit int) ([]cluster.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("job list limit must be between 1 and 100")
	}
	var jobs []cluster.Job
	err := c.doJSON(ctx, http.MethodGet, "/v1/cluster/jobs?limit="+strconv.Itoa(limit), nil, &jobs, nil)
	return jobs, err
}

func (c *clusterAPIClient) Job(ctx context.Context, jobID string) (cluster.Job, error) {
	if err := validateClusterClientID(jobID); err != nil {
		return cluster.Job{}, err
	}
	var job cluster.Job
	err := c.doJSON(ctx, http.MethodGet, "/v1/cluster/jobs/"+url.PathEscape(jobID)+"?compact=1", nil, &job, nil)
	return job, err
}

func (c *clusterAPIClient) Events(ctx context.Context, jobID string, after uint64, limit int) (cluster.JobEventPage, error) {
	if err := validateClusterClientID(jobID); err != nil {
		return cluster.JobEventPage{}, err
	}
	if limit < 1 || limit > 500 {
		return cluster.JobEventPage{}, fmt.Errorf("event limit must be between 1 and 500")
	}
	query := url.Values{}
	query.Set("after", strconv.FormatUint(after, 10))
	query.Set("limit", strconv.Itoa(limit))
	var page cluster.JobEventPage
	err := c.doJSON(ctx, http.MethodGet, "/v1/cluster/jobs/"+url.PathEscape(jobID)+"/events?"+query.Encode(), nil, &page, nil)
	return page, err
}

func (c *clusterAPIClient) Cancel(ctx context.Context, jobID string) (cluster.Job, error) {
	if err := validateClusterClientID(jobID); err != nil {
		return cluster.Job{}, err
	}
	var job cluster.Job
	err := c.doJSON(ctx, http.MethodDelete, "/v1/cluster/jobs/"+url.PathEscape(jobID), nil, &job, nil)
	return job, err
}

func validateClusterClientID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.Contains(value, "..") {
		return fmt.Errorf("job ID must use 1-128 safe characters and must not contain '..'")
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index == 0 && !alphaNumeric {
			return fmt.Errorf("job ID must start with an ASCII letter or number")
		}
		if index > 0 && !alphaNumeric && character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("job ID must use 1-128 safe ASCII characters")
		}
	}
	return nil
}

func (c *clusterAPIClient) doJSON(ctx context.Context, method, path string, input, output interface{}, headers http.Header) error {
	if c == nil || c.baseURL == "" || c.token == "" {
		return fmt.Errorf("relay URL and scoped credential are required")
	}
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := clusterHTTPClient(c.baseURL).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := readClusterAPIResponse(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(raw))
		if len(message) > 2048 {
			message = message[:2048] + "…"
		}
		return &clusterAPIError{StatusCode: response.StatusCode, Status: response.Status, Body: message}
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("invalid relay response: %w", err)
	}
	return nil
}
