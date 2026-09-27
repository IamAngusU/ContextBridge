package netpolicy

import (
	"net"
	"testing"
)

func TestTrustedPrivateEndpointIP(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{"10.42.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.20", true},
		{"fd00::42", true},
		{"169.254.10.20", true},
		{"fe80::1", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := TrustedPrivateEndpointIP(net.ParseIP(test.address)); got != test.want {
				t.Fatalf("TrustedPrivateEndpointIP(%q) = %v, want %v", test.address, got, test.want)
			}
		})
	}
}
