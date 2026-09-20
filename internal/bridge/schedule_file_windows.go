//go:build windows

package bridge

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func replaceScheduleFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// MOVEFILE_WRITE_THROUGH flushes the replacement on Windows. Opening a
// directory for fsync is not portable there, so no second durability step is
// required.
func syncScheduleDirectory(string) error { return nil }
