//go:build !windows

package config

import "os"

func replaceConfigFile(source, target string) error {
	return os.Rename(source, target)
}
