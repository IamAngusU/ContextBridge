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
	first, err := moduleComponent("github.com/coder/websocket", "v1.8.15", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := moduleComponent("github.com/coder/websocket", "v1.8.15", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.BOMRef != "pkg:golang/github.com/coder/websocket@v1.8.15" || len(first.Licenses) != 1 || first.Licenses[0].License.ID != "ISC" || !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable component: %#v / %#v", first, second)
	}
}

func TestModuleComponentRejectsUnreviewedLicense(t *testing.T) {
	if _, err := moduleComponent("example.com/new/runtime", "v1.0.0", ""); err == nil {
		t.Fatal("runtime dependency without reviewed license metadata was accepted")
	}
	component, err := moduleComponent("gopkg.in/yaml.v3", "v3.0.1", "")
	if err != nil || len(component.Licenses) != 1 || component.Licenses[0].Expression != "MIT AND Apache-2.0" {
		t.Fatalf("yaml license expression = %#v, %v", component.Licenses, err)
	}
}
