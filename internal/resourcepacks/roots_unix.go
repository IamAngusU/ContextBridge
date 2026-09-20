//go:build !windows

package resourcepacks

import (
	"os"
	"path/filepath"
)

func automaticRoots() []string {
	bases := []string{"/mnt", "/media", "/run/media", "/Volumes"}
	result := make([]string, 0)
	for _, base := range bases {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				result = append(result, filepath.Join(base, entry.Name()))
			}
		}
	}
	return result
}
