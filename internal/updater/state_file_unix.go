//go:build !windows

package updater

import "os"

func replaceStateFile(source, target string) error {
	return os.Rename(source, target)
}

func syncStateDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
