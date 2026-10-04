//go:build windows

package config

func preserveFileOwner(_, _ string) error {
	return nil
}
