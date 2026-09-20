package voice

import (
	"bytes"
	"encoding/binary"
)

// KokoroPCMSampleRate/BitsPerSample/Channels describe the raw samples
// OpenRouter's Kokoro-82M endpoint returns for response_format "pcm" —
// confirmed live via a direct curl spike-test (Content-Type header came
// back "audio/pcm;rate=24000;channels=1"; 16-bit is the only depth that
// produces a plausible duration for a known-length raw response). These
// are fixed properties of that specific endpoint/model, not something a
// caller chooses.
const (
	KokoroPCMSampleRate    = 24000
	KokoroPCMBitsPerSample = 16
	KokoroPCMChannels      = 1
)

// WrapPCMAsWAV prepends a standard 44-byte WAV (RIFF/PCM) header to raw
// little-endian PCM samples, using Kokoro's fixed format above. A WAV
// header is just a fixed-size struct describing sample rate/depth/channel
// count plus the two sizes below — cheap to compute per call, which is
// what lets the same raw bytes be wrapped once per streamed chunk (for
// immediate browser playback) and again as one header over the full
// concatenated buffer (for the persisted file), with no re-encoding.
func WrapPCMAsWAV(pcm []byte) []byte {
	const (
		sampleRate    = KokoroPCMSampleRate
		bitsPerSample = KokoroPCMBitsPerSample
		channels      = KokoroPCMChannels
	)
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	dataLen := len(pcm)

	var buf bytes.Buffer
	buf.Grow(44 + dataLen)
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataLen))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16)) // fmt chunk size
	binary.Write(&buf, binary.LittleEndian, uint16(1))  // PCM format
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(&buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataLen))
	buf.Write(pcm)
	return buf.Bytes()
}
