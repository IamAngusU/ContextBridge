//go:build linux || darwin

package main

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type processResourcePoint struct {
	CPUTimeNanoseconds uint64
	ResidentBytes      uint64
	ResidentKind       string
}

func readProcessResourcePoint() (processResourcePoint, error) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return processResourcePoint{}, err
	}
	userTime := unix.TimevalToNsec(usage.Utime)
	systemTime := unix.TimevalToNsec(usage.Stime)
	point := processResourcePoint{}
	if userTime > 0 {
		point.CPUTimeNanoseconds = uint64(userTime)
	}
	if systemTime > 0 {
		point.CPUTimeNanoseconds = saturatingPerformanceUint64Add(point.CPUTimeNanoseconds, uint64(systemTime))
	}
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			return processResourcePoint{}, err
		}
		fields := strings.Fields(string(raw))
		if len(fields) < 2 {
			return processResourcePoint{}, fmt.Errorf("invalid /proc/self/statm")
		}
		pages, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return processResourcePoint{}, err
		}
		pageSize := uint64(os.Getpagesize())
		if pageSize > 0 && pages > math.MaxUint64/pageSize {
			point.ResidentBytes = math.MaxUint64
		} else {
			point.ResidentBytes = pages * pageSize
		}
		point.ResidentKind = "current_rss"
	} else {
		// On Darwin ru_maxrss is a peak resident byte count, not a current
		// sample. Keep that distinction explicit in the machine-readable field.
		if usage.Maxrss > 0 {
			point.ResidentBytes = uint64(usage.Maxrss)
		}
		point.ResidentKind = "peak_rss"
	}
	return point, nil
}

func saturatingPerformanceUint64Add(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}
