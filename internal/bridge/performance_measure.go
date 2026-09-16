package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IamAngusU/ContextBridge/internal/cluster"
)

const measuredArtifactBytes = 64 << 10
const measuredArtifactsPerSample = 16

// MeasureArtifactVerification measures the actual bounded artifact
// normalization path (base64 decode, media/size policy, and SHA-256
// recomputation) without downloading an artifact or contacting a provider.
func MeasureArtifactVerification(ctx context.Context, samples, warmup int, concurrencies []int) ([]cluster.BridgeOperationMetric, error) {
	if ctx == nil {
		return nil, errors.New("measurement context is required")
	}
	if samples < 1 || samples > 100000 || warmup < 0 || warmup > 10000 {
		return nil, errors.New("invalid artifact measurement sample bounds")
	}
	data := make([]byte, measuredArtifactBytes)
	for index := range data {
		data[index] = byte(index % 251)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	input := []Artifact{{Name: "measurement.bin", MediaType: "application/octet-stream", DataBase64: base64.StdEncoding.EncodeToString(data)}}
	spec := OutputSpec{Mode: "text", Artifacts: true, MaxArtifactBytes: measuredArtifactBytes}
	verify := func() error {
		output := NormalizeArtifacts(input, spec)
		if len(output) != 1 || output[0].Size != len(data) || output[0].SHA256 != digest {
			return errors.New("artifact verification produced invalid evidence")
		}
		return nil
	}
	metrics := make([]cluster.BridgeOperationMetric, 0, len(concurrencies))
	seen := map[int]bool{}
	for _, concurrency := range concurrencies {
		if concurrency < 1 || concurrency > cluster.MaximumWorkerConcurrency || seen[concurrency] {
			return nil, fmt.Errorf("invalid artifact measurement concurrency %d", concurrency)
		}
		seen[concurrency] = true
		for index := 0; index < warmup; index++ {
			for inner := 0; inner < measuredArtifactsPerSample; inner++ {
				if err := verify(); err != nil {
					return nil, err
				}
			}
		}
		durations := make([]time.Duration, samples)
		started := time.Now()
		if err := runMeasuredArtifacts(ctx, samples, concurrency, func(index int) error {
			operationStarted := time.Now()
			for inner := 0; inner < measuredArtifactsPerSample; inner++ {
				if err := verify(); err != nil {
					return err
				}
			}
			durations[index] = time.Since(operationStarted) / measuredArtifactsPerSample
			return nil
		}); err != nil {
			return nil, err
		}
		wall := time.Since(started)
		throughput := 0.0
		if wall > 0 {
			throughput = float64(samples*measuredArtifactsPerSample) / wall.Seconds()
		}
		metrics = append(metrics, cluster.BridgeOperationMetric{
			Name: "artifact_verification_64kib", Concurrency: concurrency, Samples: samples, OperationsPerSample: measuredArtifactsPerSample,
			P50Microseconds:  artifactPercentile(durations, 0.50),
			P95Microseconds:  artifactPercentile(durations, 0.95),
			P99Microseconds:  artifactPercentile(durations, 0.99),
			ThroughputPerSec: throughput,
		})
	}
	return metrics, nil
}

func runMeasuredArtifacts(ctx context.Context, samples, concurrency int, operation func(int) error) error {
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
					once.Do(func() { firstErr = err; stopped.Store(true) })
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

func artifactPercentile(values []time.Duration, percentile float64) float64 {
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
