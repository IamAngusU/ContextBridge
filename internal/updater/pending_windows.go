//go:build windows

package updater

func pendingUpdateFor(_, _ string) bool  { return false }
func clearPendingUpdate(_ string) error  { return nil }
func RollbackFailedStart(_ string) error { return nil }

func rollbackFailedStartAt(_, _ string) error { return nil }
