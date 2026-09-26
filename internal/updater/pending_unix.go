//go:build !windows

package updater

import (
	"encoding/json"
	"fmt"
	"os"
)

type pendingUpdate struct {
	Version     string `json:"version"`
	FailurePath string `json:"failure_path"`
}

func pendingPath(current string) string { return current + ".update-pending.json" }

func writePendingUpdate(current, version, failurePath string) error {
	value, err := json.Marshal(pendingUpdate{Version: version, FailurePath: failurePath})
	if err != nil {
		return err
	}
	path := pendingPath(current)
	staged := path + ".tmp"
	if err := os.WriteFile(staged, value, 0600); err != nil {
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return nil
}

func pendingUpdateFor(current, version string) bool {
	value, err := os.ReadFile(pendingPath(current))
	if err != nil {
		return false
	}
	var pending pendingUpdate
	return json.Unmarshal(value, &pending) == nil && pending.Version == version
}

func clearPendingUpdate(current string) error {
	err := os.Remove(pendingPath(current))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func rollbackPendingAt(current, version string) error {
	if !pendingUpdateFor(current, version) {
		return nil
	}
	value, _ := os.ReadFile(pendingPath(current))
	var pending pendingUpdate
	_ = json.Unmarshal(value, &pending)
	if err := restorePreviousAt(current); err != nil {
		return err
	}
	if pending.FailurePath != "" {
		failure, _ := json.Marshal(map[string]string{"version": version, "error": "Updated service failed startup or health check; previous version restored."})
		if err := os.WriteFile(pending.FailurePath, failure, 0600); err != nil {
			return err
		}
	}
	return clearPendingUpdate(current)
}

func restorePreviousAt(current string) error {
	backup := current + ".previous"
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("previous executable is unavailable: %w", err)
	}
	failed := current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(current, failed); err != nil {
		return err
	}
	if err := os.Rename(backup, current); err != nil {
		_ = os.Rename(failed, current)
		return fmt.Errorf("restore previous executable: %w", err)
	}
	return nil
}

func RollbackFailedStart(version string) error {
	current, err := os.Executable()
	if err != nil {
		return err
	}
	return rollbackFailedStartAt(current, version)
}

func rollbackFailedStartAt(current, version string) error { return rollbackPendingAt(current, version) }
