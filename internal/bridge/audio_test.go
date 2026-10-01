package bridge

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func TestSpeechToTextAcceptsCompleteBoundedOggOpus(t *testing.T) {
	raw := testOggOpus(48_000)
	job := Job{Task: "speech_to_text", Audio: &AudioInput{Name: "voice.ogg", MediaType: "audio/ogg; codecs=opus",
		DataBase64: base64.StdEncoding.EncodeToString(raw)}, Output: OutputSpec{Mode: "text"}}
	if err := validateJob(job); err != nil {
		t.Fatalf("valid speech input rejected: %v", err)
	}
	info, err := InspectAudioInput(*job.Audio)
	if err != nil || info.Bytes != int64(len(raw)) || info.DurationMS != 1000 || info.MediaType != "audio/ogg; codecs=opus" {
		t.Fatalf("unexpected audio evidence: %+v, %v", info, err)
	}
	response := responseJob(job)
	if response.Audio == nil || response.Audio.DataBase64 != "" || response.Audio.MediaType != job.Audio.MediaType || job.Audio.DataBase64 == "" {
		t.Fatalf("response did not redact audio without mutating input: response=%+v input=%+v", response.Audio, job.Audio)
	}
}

func TestSpeechToTextAcceptsWhatsAppOggOpusWithoutEOSAtExactPageBoundary(t *testing.T) {
	raw := testOggOpusWithFinalFlags(48_000, 0)
	info, err := InspectAudioInput(AudioInput{Name: "voice-note.ogg", MediaType: "audio/ogg; codecs=opus",
		DataBase64: base64.StdEncoding.EncodeToString(raw)})
	if err != nil || info.Bytes != int64(len(raw)) || info.DurationMS != 1000 {
		t.Fatalf("valid WhatsApp-style stream rejected: %+v, %v", info, err)
	}
	truncated := raw[:len(raw)-1]
	if _, err := InspectAudioInput(AudioInput{Name: "voice-note.ogg", MediaType: "audio/ogg; codecs=opus",
		DataBase64: base64.StdEncoding.EncodeToString(truncated)}); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated no-EOS stream error = %v", err)
	}
}

func TestSpeechToTextEOSStillRejectsTrailingPages(t *testing.T) {
	raw := testOggOpus(48_000)
	raw = append(raw, testOggPage(0, 48_312, 0x10203040, 3, []byte{0xf8})...)
	if _, err := InspectAudioInput(AudioInput{Name: "voice-note.ogg", MediaType: "audio/ogg; codecs=opus",
		DataBase64: base64.StdEncoding.EncodeToString(raw)}); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing page after EOS error = %v", err)
	}
}

func TestSpeechToTextAudioFailsClosed(t *testing.T) {
	valid := testOggOpus(48_000)
	cases := []struct {
		name string
		job  Job
		want string
	}{
		{name: "missing", job: Job{Task: "speech_to_text", Output: OutputSpec{Mode: "text"}}, want: "requires audio"},
		{name: "wrong task", job: Job{Task: "generation", Prompt: "transcribe", Audio: &AudioInput{MediaType: "audio/ogg", DataBase64: base64.StdEncoding.EncodeToString(valid)}, Output: OutputSpec{Mode: "text"}}, want: "requires task speech_to_text"},
		{name: "wrong type", job: Job{Task: "speech_to_text", Audio: &AudioInput{MediaType: "audio/mpeg", DataBase64: base64.StdEncoding.EncodeToString(valid)}, Output: OutputSpec{Mode: "text"}}, want: "media_type"},
		{name: "bad base64", job: Job{Task: "speech_to_text", Audio: &AudioInput{MediaType: "audio/ogg", DataBase64: "%%%"}, Output: OutputSpec{Mode: "text"}}, want: "valid standard base64"},
		{name: "non canonical base64", job: Job{Task: "speech_to_text", Audio: &AudioInput{MediaType: "audio/ogg", DataBase64: "/x=="}, Output: OutputSpec{Mode: "text"}}, want: "valid standard base64"},
	}
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-1] ^= 0xff
	cases = append(cases, struct {
		name string
		job  Job
		want string
	}{name: "bad checksum", job: Job{Task: "speech_to_text", Audio: &AudioInput{MediaType: "audio/ogg", DataBase64: base64.StdEncoding.EncodeToString(corrupt)}, Output: OutputSpec{Mode: "text"}}, want: "checksum"})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validateJob(test.job); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestOggOpusDurationLimitComesFromVerifiedGranule(t *testing.T) {
	raw := testOggOpus(uint64(MaximumInputAudioDurationMS)*48 + 1)
	_, err := InspectAudioInput(AudioInput{MediaType: "audio/ogg", DataBase64: base64.StdEncoding.EncodeToString(raw)})
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("overlong stream error = %v", err)
	}
}

func TestOggOpusDurationRoundsPartialMillisecondsWithoutOverflow(t *testing.T) {
	for _, test := range []struct {
		samples uint64
		wantMS  int64
	}{
		{samples: 1, wantMS: 1},
		{samples: 47, wantMS: 1},
		{samples: 48, wantMS: 1},
		{samples: 49, wantMS: 2},
	} {
		raw := testOggOpus(test.samples)
		info, err := InspectAudioInput(AudioInput{MediaType: "audio/ogg", DataBase64: base64.StdEncoding.EncodeToString(raw)})
		if err != nil || info.DurationMS != test.wantMS {
			t.Fatalf("samples %d produced duration %d, %v; want %d", test.samples, info.DurationMS, err, test.wantMS)
		}
	}
}

func testOggOpus(samples uint64) []byte {
	return testOggOpusWithFinalFlags(samples, 0x04)
}

func testOggOpusWithFinalFlags(samples uint64, finalFlags byte) []byte {
	const preSkip = uint16(312)
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8], head[9] = 1, 1
	binary.LittleEndian.PutUint16(head[10:12], preSkip)
	binary.LittleEndian.PutUint32(head[12:16], 48_000)
	tags := make([]byte, 16)
	copy(tags, "OpusTags")
	serial := uint32(0x10203040)
	result := testOggPage(0x02, 0, serial, 0, head)
	result = append(result, testOggPage(0, 0, serial, 1, tags)...)
	result = append(result, testOggPage(finalFlags, uint64(preSkip)+samples, serial, 2, []byte{0xf8})...)
	return result
}

func testOggPage(flags byte, granule uint64, serial, sequence uint32, packet []byte) []byte {
	if len(packet) >= 255 {
		panic("test packet too large")
	}
	page := make([]byte, 28+len(packet))
	copy(page, "OggS")
	page[4], page[5] = 0, flags
	binary.LittleEndian.PutUint64(page[6:14], granule)
	binary.LittleEndian.PutUint32(page[14:18], serial)
	binary.LittleEndian.PutUint32(page[18:22], sequence)
	page[26], page[27] = 1, byte(len(packet))
	copy(page[28:], packet)
	binary.LittleEndian.PutUint32(page[22:26], oggCRC(page))
	return page
}
