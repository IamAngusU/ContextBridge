//go:build windows

package updater

import "errors"

func RestartCurrentProcess() error {
	return errors.New("the Windows update helper will restart the managed process after replacement")
}

func restartCurrentProcessAt(_ string) error { return RestartCurrentProcess() }
