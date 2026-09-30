package bridge

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"mime"
	"strings"
	"unicode"
)

// AudioInfo is derived from the admitted bytes, never trusted from producer
// metadata. It can be copied into authenticated cluster requirements.
type AudioInfo struct {
	MediaType  string
	Bytes      int64
	DurationMS int64
}

// InspectAudioInput validates the complete v1 audio contract and returns its
// routing metadata. Ogg checksums, page order, Opus headers and the terminal
// granule position are all verified without decoding attacker-controlled audio.
func InspectAudioInput(input AudioInput) (AudioInfo, error) {
	if len(input.Name) > 255 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 {
		return AudioInfo{}, errors.New("audio.name must be at most 255 bytes without control characters")
	}
	mediaType, params, err := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(input.MediaType)))
	if err != nil || mediaType != "audio/ogg" {
		return AudioInfo{}, errors.New("audio.media_type must be audio/ogg or audio/ogg; codecs=opus")
	}
	for name, value := range params {
		if name != "codecs" || !strings.EqualFold(strings.TrimSpace(value), "opus") {
			return AudioInfo{}, errors.New("audio.media_type must be audio/ogg or audio/ogg; codecs=opus")
		}
	}
	if input.DataBase64 == "" {
		return AudioInfo{}, errors.New("audio.data_base64 is required")
	}
	if len(input.DataBase64) > base64.StdEncoding.EncodedLen(MaximumInputAudioBytes) {
		return AudioInfo{}, fmt.Errorf("audio exceeds the %d MiB decoded limit", MaximumInputAudioBytes>>20)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(input.DataBase64)
	if err != nil {
		return AudioInfo{}, errors.New("audio.data_base64 must contain valid standard base64")
	}
	if len(decoded) == 0 || len(decoded) > MaximumInputAudioBytes {
		return AudioInfo{}, fmt.Errorf("audio must be non-empty and no larger than %d MiB", MaximumInputAudioBytes>>20)
	}
	durationMS, err := inspectOggOpus(decoded)
	if err != nil {
		return AudioInfo{}, fmt.Errorf("audio bytes are not a complete supported Ogg/Opus recording: %w", err)
	}
	if durationMS > MaximumInputAudioDurationMS {
		return AudioInfo{}, fmt.Errorf("audio exceeds the %d minute duration limit", MaximumInputAudioDurationMS/60000)
	}
	return AudioInfo{MediaType: "audio/ogg; codecs=opus", Bytes: int64(len(decoded)), DurationMS: durationMS}, nil
}

func validateAudioInput(input AudioInput) error {
	_, err := InspectAudioInput(input)
	return err
}

const oggNoGranule = ^uint64(0)

func inspectOggOpus(data []byte) (int64, error) {
	var (
		offset       int
		serial       uint32
		nextSequence uint32
		packet       []byte
		packetCount  int
		preSkip      uint64
		lastGranule  = oggNoGranule
		priorGranule = oggNoGranule
		seenEOS      bool
	)
	for offset < len(data) {
		if len(data)-offset < 27 || !bytes.Equal(data[offset:offset+4], []byte("OggS")) || data[offset+4] != 0 {
			return 0, errors.New("invalid Ogg page header")
		}
		headerType := data[offset+5]
		if headerType&^byte(0x07) != 0 || seenEOS {
			return 0, errors.New("invalid Ogg page flags or trailing stream")
		}
		granule := binary.LittleEndian.Uint64(data[offset+6 : offset+14])
		pageSerial := binary.LittleEndian.Uint32(data[offset+14 : offset+18])
		sequence := binary.LittleEndian.Uint32(data[offset+18 : offset+22])
		segments := int(data[offset+26])
		if len(data)-offset < 27+segments {
			return 0, errors.New("truncated Ogg segment table")
		}
		bodyBytes := 0
		for _, size := range data[offset+27 : offset+27+segments] {
			bodyBytes += int(size)
		}
		pageBytes := 27 + segments + bodyBytes
		if len(data)-offset < pageBytes {
			return 0, errors.New("truncated Ogg page body")
		}
		page := data[offset : offset+pageBytes]
		wantCRC := binary.LittleEndian.Uint32(page[22:26])
		if oggCRC(page) != wantCRC {
			return 0, errors.New("invalid Ogg checksum")
		}
		continued := headerType&0x01 != 0
		if offset == 0 {
			if headerType&0x02 == 0 || continued || sequence != 0 {
				return 0, errors.New("invalid Ogg beginning-of-stream page")
			}
			serial = pageSerial
		} else if headerType&0x02 != 0 || pageSerial != serial || sequence != nextSequence || continued != (len(packet) > 0) {
			return 0, errors.New("invalid Ogg stream or page sequence")
		}
		nextSequence = sequence + 1
		bodyOffset := offset + 27 + segments
		for _, sizeByte := range data[offset+27 : offset+27+segments] {
			size := int(sizeByte)
			packet = append(packet, data[bodyOffset:bodyOffset+size]...)
			bodyOffset += size
			if size < 255 {
				packetCount++
				switch packetCount {
				case 1:
					if len(packet) < 19 || !bytes.Equal(packet[:8], []byte("OpusHead")) || packet[8] == 0 || packet[9] == 0 {
						return 0, errors.New("invalid Opus identification header")
					}
					preSkip = uint64(binary.LittleEndian.Uint16(packet[10:12]))
				case 2:
					if len(packet) < 16 || !bytes.Equal(packet[:8], []byte("OpusTags")) {
						return 0, errors.New("invalid Opus comment header")
					}
				default:
					if len(packet) == 0 {
						return 0, errors.New("empty Opus audio packet")
					}
				}
				packet = packet[:0]
			}
		}
		if granule != oggNoGranule {
			if priorGranule != oggNoGranule && granule < priorGranule {
				return 0, errors.New("non-monotonic Ogg granule position")
			}
			priorGranule = granule
			lastGranule = granule
			maximumSamples := uint64(MaximumInputAudioDurationMS) * 48
			if granule > preSkip+maximumSamples {
				return 0, errors.New("Opus duration exceeds protocol limit")
			}
		}
		seenEOS = headerType&0x04 != 0
		offset += pageBytes
	}
	if !seenEOS || len(packet) != 0 || packetCount < 3 || lastGranule == oggNoGranule || lastGranule <= preSkip {
		return 0, errors.New("incomplete Ogg/Opus stream")
	}
	samples := lastGranule - preSkip
	// Opus granules are expressed at 48 kHz, so one millisecond is exactly
	// 48 samples. Divide before converting instead of multiplying by 1000:
	// besides being simpler, this keeps even a hostile uint64 granule from
	// overflowing before the duration bound above can reject it.
	durationMS := samples / 48
	if samples%48 != 0 {
		durationMS++
	}
	const maximumInt64 = uint64(1<<63 - 1)
	if durationMS > maximumInt64 {
		return 0, errors.New("Opus duration exceeds integer range")
	}
	return int64(durationMS), nil
}

func oggCRC(page []byte) uint32 {
	var crc uint32
	for index, value := range page {
		if index >= 22 && index < 26 {
			value = 0
		}
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^value]
	}
	return crc
}

var oggCRCTable = func() [256]uint32 {
	var table [256]uint32
	for index := range table {
		value := uint32(index) << 24
		for range 8 {
			if value&0x80000000 != 0 {
				value = (value << 1) ^ 0x04c11db7
			} else {
				value <<= 1
			}
		}
		table[index] = value
	}
	return table
}()
