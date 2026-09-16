package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// BridgeMeasurementOptions controls the isolated ContextBridge-only
// measurement. It never starts a worker, browser, model runtime, or provider.
type BridgeMeasurementOptions struct {
	Samples       int
	Warmup        int
	Concurrencies []int
	DatabaseJobs  int
}

// BridgeOperationMetric is one measured distribution. Latencies describe one
// complete logical operation, while throughput covers the whole concurrent
// sample window. Neither value includes provider work.
type BridgeOperationMetric struct {
	Name                string  `json:"name"`
	Concurrency         int     `json:"concurrency"`
	Samples             int     `json:"samples"`
	OperationsPerSample int     `json:"operations_per_sample"`
	P50Microseconds     float64 `json:"p50_us"`
	P95Microseconds     float64 `json:"p95_us"`
	P99Microseconds     float64 `json:"p99_us"`
	ThroughputPerSec    float64 `json:"throughput_ops_per_second"`
}

// DatabaseGrowthMetric records allocated Bolt file bytes, not an estimate of
// logical record size. Bolt reuses freed pages and does not compact on delete,
// so this is intentionally measured from a new database every run.
type DatabaseGrowthMetric struct {
	Jobs                  int     `json:"jobs"`
	BaselineBytes         int64   `json:"baseline_bytes"`
	AfterBytes            int64   `json:"after_bytes"`
	GrowthBytes           int64   `json:"growth_bytes"`
	GrowthPerJobBytes     float64 `json:"growth_per_job_bytes"`
	GrowthPer1000JobBytes float64 `json:"growth_per_1000_jobs_bytes"`
}

// BridgeMeasurement contains only isolated relay and cryptographic work.
type BridgeMeasurement struct {
	Operations []BridgeOperationMetric `json:"operations"`
	Database   DatabaseGrowthMetric    `json:"database"`
}

// MeasureBridgeOnly measures current ContextBridge primitives with fresh
// temporary databases and loopback HTTP servers. It is deliberately a runtime
// measurement rather than a test benchmark so operators can export one
// machine-readable report from a release binary.
func MeasureBridgeOnly(ctx context.Context, options BridgeMeasurementOptions) (BridgeMeasurement, error) {
	if ctx == nil {
		return BridgeMeasurement{}, errors.New("measurement context is required")
	}
	if options.Samples <= 0 || options.Samples > 100000 {
		return BridgeMeasurement{}, errors.New("samples must be between 1 and 100000")
	}
	if options.Warmup < 0 || options.Warmup > 10000 {
		return BridgeMeasurement{}, errors.New("warmup must be between 0 and 10000")
	}
	if options.DatabaseJobs <= 0 || options.DatabaseJobs > 100000 {
		return BridgeMeasurement{}, errors.New("database jobs must be between 1 and 100000")
	}
	if len(options.Concurrencies) == 0 {
		return BridgeMeasurement{}, errors.New("at least one concurrency is required")
	}
	seen := map[int]bool{}
	for _, concurrency := range options.Concurrencies {
		if concurrency < 1 || concurrency > MaximumWorkerConcurrency {
			return BridgeMeasurement{}, fmt.Errorf("concurrency must be between 1 and %d", MaximumWorkerConcurrency)
		}
		if seen[concurrency] {
			return BridgeMeasurement{}, fmt.Errorf("duplicate concurrency %d", concurrency)
		}
		seen[concurrency] = true
	}

	result := BridgeMeasurement{}
	for _, concurrency := range options.Concurrencies {
		queue, err := measureRelayQueueCycles(ctx, options.Samples, options.Warmup, concurrency)
		if err != nil {
			return BridgeMeasurement{}, err
		}
		result.Operations = append(result.Operations, queue)

		e2ee, err := measureE2EECycles(ctx, options.Samples, options.Warmup, concurrency)
		if err != nil {
			return BridgeMeasurement{}, err
		}
		result.Operations = append(result.Operations, e2ee)
	}
	database, err := measureDatabaseGrowth(ctx, options.DatabaseJobs)
	if err != nil {
		return BridgeMeasurement{}, err
	}
	result.Database = database
	return result, nil
}

func measureRelayQueueCycles(ctx context.Context, samples, warmup, concurrency int) (BridgeOperationMetric, error) {
	directory, err := os.MkdirTemp("", "contextbridge-measure-relay-")
	if err != nil {
		return BridgeOperationMetric{}, err
	}
	defer os.RemoveAll(directory)

	relay, err := NewRelay(RelayConfig{
		Database:      filepath.Join(directory, "relay.db"),
		AdminToken:    "measure_admin_012345678901234567890123456789",
		AllowedTasks:  []string{"generation"},
		MaxQueuedJobs: max(10000, samples+warmup+1),
	}, nil)
	if err != nil {
		return BridgeOperationMetric{}, err
	}
	defer relay.Close()
	server := httptest.NewServer(relay.Handler())
	defer server.Close()
	producer, _, err := relay.store.CreateToken("producer", "measurement-producer", nil, time.Hour)
	if err != nil {
		return BridgeOperationMetric{}, err
	}
	client := server.Client()

	for index := 0; index < warmup; index++ {
		if err := relayQueueCycle(ctx, client, server.URL, producer, fmt.Sprintf("warm-%d-%d", concurrency, index)); err != nil {
			return BridgeOperationMetric{}, fmt.Errorf("relay queue warmup: %w", err)
		}
	}
	durations := make([]time.Duration, samples)
	started := time.Now()
	err = runConcurrentSamples(ctx, samples, concurrency, func(index int) error {
		operationStarted := time.Now()
		if err := relayQueueCycle(ctx, client, server.URL, producer, fmt.Sprintf("sample-%d-%d", concurrency, index)); err != nil {
			return err
		}
		durations[index] = time.Since(operationStarted)
		return nil
	})
	wall := time.Since(started)
	if err != nil {
		return BridgeOperationMetric{}, fmt.Errorf("relay queue measurement at concurrency %d: %w", concurrency, err)
	}
	return operationMetric("relay_queue_submit_read_cancel", concurrency, durations, wall, 1), nil
}

func relayQueueCycle(ctx context.Context, client *http.Client, baseURL, token, id string) error {
	request := SubmitRequest{
		ID: id, Source: "performance-measurement",
		Requirements: Requirements{Task: "generation"},
		Payload:      json.RawMessage(`{"prompt":"CB-MEASURE","output":{"mode":"text","max_bytes":1024}}`),
		MaxAttempts:  1,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var admitted Job
	if err := measurementJSONRequest(ctx, client, http.MethodPost, baseURL+"/v1/cluster/jobs?compact=1", token, body, http.StatusAccepted, &admitted); err != nil {
		return err
	}
	if admitted.ID != id || admitted.Status != JobQueued {
		return errors.New("relay returned an invalid admitted job")
	}
	var readback Job
	if err := measurementJSONRequest(ctx, client, http.MethodGet, baseURL+"/v1/cluster/jobs/"+id+"?compact=1", token, nil, http.StatusOK, &readback); err != nil {
		return err
	}
	if readback.ID != id || readback.Status != JobQueued {
		return errors.New("relay returned an invalid compact readback")
	}
	var cancelled Job
	if err := measurementJSONRequest(ctx, client, http.MethodDelete, baseURL+"/v1/cluster/jobs/"+id, token, nil, http.StatusOK, &cancelled); err != nil {
		return err
	}
	if cancelled.ID != id || cancelled.Status != JobCancelled {
		return errors.New("relay returned an invalid cancellation")
	}
	return nil
}

func measurementJSONRequest(ctx context.Context, client *http.Client, method, url, token string, body []byte, status int, target interface{}) error {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%s %s returned %s: %s", method, url, response.Status, bytes.TrimSpace(raw))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return err
	}
	return nil
}

func measureE2EECycles(ctx context.Context, samples, warmup, concurrency int) (BridgeOperationMetric, error) {
	const operationsPerSample = 16
	privateKey, publicKey, err := NewIdentity()
	if err != nil {
		return BridgeOperationMetric{}, err
	}
	payload := []byte(`{"prompt":"CB-MEASURE"}`)
	result := []byte(`{"output":{"mode":"text","text":"CB-MEASURE"}}`)
	cycle := func(index int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		aad := []byte(fmt.Sprintf("contextbridge-measure-job-%d", index))
		resultAAD := []byte(fmt.Sprintf("contextbridge-measure-result-%d", index))
		envelope, producerShared, err := SealFor(publicKey, payload, aad)
		if err != nil {
			return err
		}
		opened, workerShared, err := OpenWith(privateKey, envelope, aad)
		if err != nil {
			return fmt.Errorf("worker open failed: %w", err)
		}
		if !bytes.Equal(opened, payload) {
			return errors.New("worker opened a different job payload")
		}
		sealedResult, err := SealResponse(workerShared, result, resultAAD)
		if err != nil {
			return err
		}
		openedResult, err := OpenResponse(producerShared, sealedResult, resultAAD)
		if err != nil {
			return fmt.Errorf("producer open failed: %w", err)
		}
		if !bytes.Equal(openedResult, result) {
			return errors.New("producer opened a different response payload")
		}
		return nil
	}
	for index := 0; index < warmup; index++ {
		for inner := 0; inner < operationsPerSample; inner++ {
			if err := cycle(-((index*operationsPerSample + inner) + 1)); err != nil {
				return BridgeOperationMetric{}, fmt.Errorf("e2ee warmup: %w", err)
			}
		}
	}
	durations := make([]time.Duration, samples)
	started := time.Now()
	err = runConcurrentSamples(ctx, samples, concurrency, func(index int) error {
		operationStarted := time.Now()
		for inner := 0; inner < operationsPerSample; inner++ {
			if err := cycle(index*operationsPerSample + inner); err != nil {
				return err
			}
		}
		durations[index] = time.Since(operationStarted) / operationsPerSample
		return nil
	})
	wall := time.Since(started)
	if err != nil {
		return BridgeOperationMetric{}, fmt.Errorf("e2ee measurement at concurrency %d: %w", concurrency, err)
	}
	return operationMetric("e2ee_small_job_and_result", concurrency, durations, wall, operationsPerSample), nil
}

func runConcurrentSamples(ctx context.Context, samples, concurrency int, operation func(int) error) error {
	work := make(chan int)
	var stopped atomic.Bool
	var workers sync.WaitGroup
	var once sync.Once
	var firstErr error
	workers.Add(concurrency)
	for worker := 0; worker < concurrency; worker++ {
		go func() {
			defer workers.Done()
			for index := range work {
				if stopped.Load() {
					continue
				}
				if err := operation(index); err != nil {
					once.Do(func() {
						firstErr = err
						stopped.Store(true)
					})
				}
			}
		}()
	}
	for index := 0; index < samples; index++ {
		if stopped.Load() {
			break
		}
		select {
		case <-ctx.Done():
			once.Do(func() { firstErr = ctx.Err(); stopped.Store(true) })
		case work <- index:
		}
	}
	close(work)
	workers.Wait()
	return firstErr
}

func operationMetric(name string, concurrency int, durations []time.Duration, wall time.Duration, operationsPerSample int) BridgeOperationMetric {
	throughput := 0.0
	if wall > 0 {
		throughput = float64(len(durations)*operationsPerSample) / wall.Seconds()
	}
	return BridgeOperationMetric{
		Name: name, Concurrency: concurrency, Samples: len(durations), OperationsPerSample: operationsPerSample,
		P50Microseconds:  durationPercentile(durations, 0.50),
		P95Microseconds:  durationPercentile(durations, 0.95),
		P99Microseconds:  durationPercentile(durations, 0.99),
		ThroughputPerSec: throughput,
	}
}

func durationPercentile(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered))*percentile+0.999999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return float64(ordered[index]) / float64(time.Microsecond)
}

func measureDatabaseGrowth(ctx context.Context, jobs int) (DatabaseGrowthMetric, error) {
	directory, err := os.MkdirTemp("", "contextbridge-measure-database-")
	if err != nil {
		return DatabaseGrowthMetric{}, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "relay.db")
	store, err := OpenStore(path)
	if err != nil {
		return DatabaseGrowthMetric{}, err
	}
	defer store.Close()
	if err := store.db.Sync(); err != nil {
		return DatabaseGrowthMetric{}, err
	}
	baseline, err := os.Stat(path)
	if err != nil {
		return DatabaseGrowthMetric{}, err
	}
	payload := json.RawMessage(`{"prompt":"CB-MEASURE","output":{"mode":"text","max_bytes":1024}}`)
	for index := 0; index < jobs; index++ {
		if err := ctx.Err(); err != nil {
			return DatabaseGrowthMetric{}, err
		}
		id := fmt.Sprintf("measure-%06d", index)
		job, err := store.CreateJob(SubmitRequest{
			ID: id, OwnerSubject: "measurement-producer", Source: "performance-measurement",
			Requirements: Requirements{Task: "generation"}, Payload: payload, MaxAttempts: 1,
		})
		if err != nil {
			return DatabaseGrowthMetric{}, err
		}
		if _, err := store.CancelJob(job.ID); err != nil {
			return DatabaseGrowthMetric{}, err
		}
	}
	if err := store.db.Sync(); err != nil {
		return DatabaseGrowthMetric{}, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return DatabaseGrowthMetric{}, err
	}
	growth := max(int64(0), after.Size()-baseline.Size())
	perJob := float64(growth) / float64(jobs)
	return DatabaseGrowthMetric{
		Jobs: jobs, BaselineBytes: baseline.Size(), AfterBytes: after.Size(), GrowthBytes: growth,
		GrowthPerJobBytes: perJob, GrowthPer1000JobBytes: perJob * 1000,
	}, nil
}
