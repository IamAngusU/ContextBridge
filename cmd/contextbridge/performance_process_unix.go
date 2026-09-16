//go:build linux || darwin

package main

import (
	"fmt"
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
	point := processResourcePoint{
		CPUTimeNanoseconds: uint64(unix.TimevalToNsec(usage.Utime) + unix.TimevalToNsec(usage.Stime)),
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
		point.ResidentBytes = pages * uint64(os.Getpagesize())
		point.ResidentKind = "current_rss"
	} else {
		// On Darwin ru_maxrss is a peak resident byte count, not a current
		// sample. Keep that distinction explicit in the machine-readable field.
		point.ResidentBytes = uint64(usage.Maxrss)
		point.ResidentKind = "peak_rss"
	}
	return point, nil
}
