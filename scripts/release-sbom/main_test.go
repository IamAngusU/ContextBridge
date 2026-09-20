package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"testing"
)

func TestGoModuleDigest(t *testing.T) {
	digest := sha256.Sum256([]byte("module"))
	encoded := "h1:" + base64.StdEncoding.EncodeToString(digest[:])
	got, ok := goModuleDigest(encoded)
	if !ok || got != hex.EncodeToString(digest[:]) {
		t.Fatalf("digest = %q, %v", got, ok)
	}
	for _, invalid := range []string{"", "sha256:test", "h1:not-base64", "h1:" + base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, ok := goModuleDigest(invalid); ok {
			t.Fatalf("invalid module sum accepted: %q", invalid)
		}
	}
}

func TestModuleComponentIsStable(t *testing.T) {
	first := moduleComponent("example.com/owner/module", "v1.2.3", "")
	second := moduleComponent("example.com/owner/module", "v1.2.3", "")
	if first.BOMRef != "pkg:golang/example.com/owner/module@v1.2.3" || !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable component: %#v / %#v", first, second)
	}
}
