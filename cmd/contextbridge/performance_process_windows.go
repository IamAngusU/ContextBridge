//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processResourcePoint struct {
	CPUTimeNanoseconds uint64
	ResidentBytes      uint64
	ResidentKind       string
}

type processMemoryCountersEx struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func readProcessResourcePoint() (processResourcePoint, error) {
	handle := windows.CurrentProcess()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return processResourcePoint{}, err
	}
	counters := processMemoryCountersEx{Size: uint32(unsafe.Sizeof(processMemoryCountersEx{}))}
	result, _, callErr := getProcessMemoryInfo.Call(
		uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size),
	)
	if result == 0 {
		return processResourcePoint{}, fmt.Errorf("GetProcessMemoryInfo: %w", callErr)
	}
	return processResourcePoint{
		CPUTimeNanoseconds: filetimeDurationNanoseconds(kernel) + filetimeDurationNanoseconds(user),
		ResidentBytes:      uint64(counters.WorkingSetSize),
		ResidentKind:       "current_working_set",
	}, nil
}

func filetimeDurationNanoseconds(value windows.Filetime) uint64 {
	ticks := uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
	return ticks * 100
}
