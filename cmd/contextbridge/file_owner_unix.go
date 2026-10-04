//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func matchFileOwner(reference, target string) error {
	referenceInfo, err := os.Lstat(reference)
	if err != nil {
		return err
	}
	targetInfo, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if referenceInfo.Mode()&os.ModeSymlink != 0 || !referenceInfo.Mode().IsRegular() || targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() {
		return errors.New("owner matching requires regular non-symlink files")
	}
	referenceMetadata, referenceOK := referenceInfo.Sys().(*syscall.Stat_t)
	targetMetadata, targetOK := targetInfo.Sys().(*syscall.Stat_t)
	if !referenceOK || !targetOK {
		return errors.New("file owner metadata is unavailable")
	}
	if referenceMetadata.Uid == targetMetadata.Uid && referenceMetadata.Gid == targetMetadata.Gid {
		return nil
	}
	if err := os.Chown(target, int(referenceMetadata.Uid), int(referenceMetadata.Gid)); err != nil {
		return fmt.Errorf("set file owner: %w", err)
	}
	return nil
}
