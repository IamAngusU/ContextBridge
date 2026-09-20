package cluster

import (
	"reflect"
	"testing"
)

func TestIndicatorsHaveStableOrderAndNoAssumedMedia(t *testing.T) {
	capability := Capabilities{
		Providers: []string{"llama_cpp", "adapter"},
		AdapterSessions: []AdapterSessionCapability{
			{Profile: "profile-two"}, {Profile: "profile-one"},
		},
		Tasks:  []string{"video_generation", "music", "generation", "embedding"},
		Models: []ModelCapability{{Vision: true}},
	}
	if got, want := IndicatorSources(capability), []string{"adapter", "local"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	if got, want := IndicatorModes(capability), []string{"text", "vision", "music", "video", "embedding"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("modes = %v, want %v", got, want)
	}
	if got := IndicatorModes(Capabilities{AdapterSessions: []AdapterSessionCapability{{Profile: "profile-one"}}}); !reflect.DeepEqual(got, []string{"text"}) {
		t.Fatalf("adapter media must not be guessed: %v", got)
	}
}

func TestNodeDiscriminatorIsStableAndDisplayOnly(t *testing.T) {
	if got := NodeDiscriminator("node_00000000000000000000000000abcdef"); got != "abcdef" {
		t.Fatalf("discriminator = %q", got)
	}
	if got := NodeDiscriminator("node_A1"); got != "nodea1" {
		t.Fatalf("short discriminator = %q", got)
	}
}
