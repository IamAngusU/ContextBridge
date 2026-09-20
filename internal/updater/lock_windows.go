//go:build windows

package updater

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

func lockProcessAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > uint64(math.MaxUint32) {
		return false
	}
	// #nosec G115 -- the explicit bounds check above proves the Windows PID fits.
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied is not proof that the owner exited.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if windows.GetExitCodeProcess(handle, &exitCode) != nil {
		return true
	}
	return exitCode == 259 // STILL_ACTIVE
}
