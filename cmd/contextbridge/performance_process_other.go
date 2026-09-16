//go:build !windows && !linux && !darwin

package main

import "errors"

type processResourcePoint struct {
	CPUTimeNanoseconds uint64
	ResidentBytes      uint64
	ResidentKind       string
}

func readProcessResourcePoint() (processResourcePoint, error) {
	return processResourcePoint{}, errors.New("process CPU and resident memory sampling is unsupported on this OS")
}
