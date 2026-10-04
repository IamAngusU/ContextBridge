//go:build windows

package main

func matchFileOwner(_, _ string) error {
	return nil
}
