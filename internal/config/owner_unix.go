//go:build !windows

package config

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func preserveFileOwner(reference, target string) error {
	info, err := os.Lstat(reference)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("existing config must be a regular non-symlink file")
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("existing config owner metadata is unavailable")
	}
	targetInfo, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
		return errors.New("temporary config must be a regular non-symlink file")
	}
	targetMetadata, ok := targetInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("temporary config owner metadata is unavailable")
	}
	if metadata.Uid == targetMetadata.Uid && metadata.Gid == targetMetadata.Gid {
		return nil
	}
	if err := os.Chown(target, int(metadata.Uid), int(metadata.Gid)); err != nil {
		return fmt.Errorf("preserve existing config owner: %w", err)
	}
	return nil
}
