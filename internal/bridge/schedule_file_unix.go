//go:build !windows

package bridge

import (
	"os"
)

func replaceScheduleFile(source, target string) error {
	return os.Rename(source, target)
}

func syncScheduleDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
