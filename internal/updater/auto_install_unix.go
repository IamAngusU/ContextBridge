//go:build !windows

package updater

import "context"

func automaticInstallReady(context.Context, string) bool { return true }
