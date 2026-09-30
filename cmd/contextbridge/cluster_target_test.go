package main

import "testing"

func TestValidateClusterAPITarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "remote HTTPS", target: "https://relay.example/v1/cluster/nodes?compact=1"},
		{name: "loopback HTTP", target: "http://127.0.0.1:32150/v1/cluster/nodes"},
		{name: "remote cleartext", target: "http://relay.example/v1/cluster/nodes", wantErr: true},
		{name: "userinfo", target: "https://token@relay.example/v1/cluster/nodes", wantErr: true},
		{name: "fragment", target: "https://relay.example/v1/cluster/nodes#secret", wantErr: true},
		{name: "relative", target: "/v1/cluster/nodes", wantErr: true},
		{name: "surrounding whitespace", target: " https://relay.example/v1/cluster/nodes", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateClusterAPITarget(test.target)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateClusterAPITarget(%q) error = %v, wantErr %v", test.target, err, test.wantErr)
			}
		})
	}
}
