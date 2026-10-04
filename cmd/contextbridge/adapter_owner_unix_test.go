//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/IamAngusU/ContextBridge/internal/config"
)

func TestRootAdapterSetupKeepsServiceOwnedConfigAndCredential(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yml")
	credentialPath := filepath.Join(directory, "workspace.token")
	if err := config.Default(configPath); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(configPath, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapterSetupCommand([]string{
		"workspace-local", "--config", configPath, "--driver", "contextbridge-workspace",
		"--task", "generation", "--token-file", credentialPath, "--create-token",
	}); err != nil {
		t.Fatal(err)
	}
	configInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	credentialInfo, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	configOwner := configInfo.Sys().(*syscall.Stat_t)
	credentialOwner := credentialInfo.Sys().(*syscall.Stat_t)
	if configOwner.Uid != credentialOwner.Uid || configOwner.Gid != credentialOwner.Gid {
		t.Fatalf("config owner %d:%d differs from credential owner %d:%d", configOwner.Uid, configOwner.Gid, credentialOwner.Uid, credentialOwner.Gid)
	}
	if configInfo.Mode().Perm() != 0600 || credentialInfo.Mode().Perm() != 0600 {
		t.Fatalf("config/credential modes are %o/%o, want 600/600", configInfo.Mode().Perm(), credentialInfo.Mode().Perm())
	}
}
