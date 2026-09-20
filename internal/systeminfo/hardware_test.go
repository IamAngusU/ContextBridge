package systeminfo

import "testing"

func TestParseNVIDIAGPUsKeepsEveryDeviceAndQuotedNames(t *testing.T) {
	raw := []byte("NVIDIA RTX 3080, 10240, 2048, 8192, 600.01, 71, 83\n\"NVIDIA Test, Rack GPU\", 24576, 1024, 23552, 600.01, 44, 12\n")
	gpus := parseNVIDIAGPUs(raw)
	if len(gpus) != 2 {
		t.Fatalf("parsed %d NVIDIA GPUs, want 2: %#v", len(gpus), gpus)
	}
	if gpus[1].Name != "NVIDIA Test, Rack GPU" || gpus[1].MemoryFree != 23552*1024*1024 || gpus[0].Utilization != 83 {
		t.Fatalf("unexpected NVIDIA inventory: %#v", gpus)
	}
}

func TestParseNVIDIAGPUsRejectsMalformedCSV(t *testing.T) {
	if got := parseNVIDIAGPUs([]byte("\"unterminated, 1, 2\n")); got != nil {
		t.Fatalf("malformed CSV returned GPUs: %#v", got)
	}
}

func TestParseROCmGPUsKeepsStableMultiDeviceOrder(t *testing.T) {
	raw := []byte(`{
  "card2":{"Card series":"AMD B","VRAM Total Memory (B)":"200","VRAM Total Used Memory (B)":"40"},
  "card0":{"Card series":"AMD A","VRAM Total Memory (B)":"100","VRAM Total Used Memory (B)":"25"}
}`)
	gpus := parseROCmGPUs(raw)
	if len(gpus) != 2 || gpus[0].Name != "AMD A" || gpus[0].MemoryFree != 75 || gpus[1].Name != "AMD B" || gpus[1].MemoryFree != 160 {
		t.Fatalf("unexpected ROCm inventory: %#v", gpus)
	}
}

func TestSubtractNeverUnderflowsBrokenDriverCounters(t *testing.T) {
	if got := subtract(10, 11); got != 0 {
		t.Fatalf("free memory underflowed: %d", got)
	}
}
