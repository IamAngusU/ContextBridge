package cluster

import (
	"reflect"
	"testing"
)

func TestIndicatorsHaveStableOrderAndNoAssumedMedia(t *testing.T) {
	capability := Capabilities{
		Providers: []string{"llama_cpp", "browser"},
		BrowserSessions: []BrowserSessionCapability{
			{Profile: "gemini"}, {Profile: "chatgpt"},
		},
		Tasks:  []string{"video_generation", "music", "generation", "embedding"},
		Models: []ModelCapability{{Vision: true}},
	}
	if got, want := IndicatorSources(capability), []string{"chatgpt", "gemini", "local"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	if got, want := IndicatorModes(capability), []string{"text", "vision", "music", "video", "embedding"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("modes = %v, want %v", got, want)
	}
	if got := IndicatorModes(Capabilities{BrowserSessions: []BrowserSessionCapability{{Profile: "chatgpt"}}}); !reflect.DeepEqual(got, []string{"text"}) {
		t.Fatalf("browser media must not be guessed: %v", got)
	}
}

func TestNodeDiscriminatorIsStableAndDisplayOnly(t *testing.T) {
	if got := NodeDiscriminator("node_5a18aecdcb05db6887c355031ad5ca35"); got != "d5ca35" {
		t.Fatalf("discriminator = %q", got)
	}
	if got := NodeDiscriminator("node_A1"); got != "nodea1" {
		t.Fatalf("short discriminator = %q", got)
	}
}
