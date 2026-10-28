package embed

import "math"

// int8Scale is the quantization step: a stored byte is the rounded value
// times 128, giving a representable range of [-1, 0.9921875] at a
// resolution of 1/128 ≈ 0.0078.
//
// This is not a lossy-tradeoff-by-default in the way int8 quantization
// usually is. perplexity/pplx-embed-v1-0.6b emits values that are *exactly*
// multiples of 1/128 (confirmed across 300k+ components of real message
// embeddings: zero deviation), so for that model encode/decode is
// bit-exact — 1,024 bytes per vector instead of 4,096, for free. For a
// provider whose output is full-precision float (local
// nomic-embed-text, whose values are L2-normalized and span ±0.24), the
// same encoding introduces at most 1/256 per component. That perturbs
// retrieval *ranking* by a few percent of the cosine score — irrelevant
// next to the gaps between relevant and irrelevant results — and never
// touches the query-similarity signal, which compares live in-memory
// vectors and never round-trips through storage.
const int8Scale = 128

// EncodeInt8 packs v into one byte per component. The stored blob's own
// length is the vector's dimension count, so no separate width bookkeeping
// is needed alongside it.
func EncodeInt8(v []float32) []byte {
	out := make([]byte, len(v))
	for i, x := range v {
		q := int(math.Round(float64(x) * int8Scale))
		// Clamp rather than wrap: a value outside ±1 would otherwise
		// convert with implementation-defined sign flipping in Go, turning
		// one large component into a wildly wrong one instead of merely a
		// saturated one.
		if q > math.MaxInt8 {
			q = math.MaxInt8
		} else if q < math.MinInt8 {
			q = math.MinInt8
		}
		out[i] = byte(int8(q))
	}
	return out
}

// DecodeInt8 unpacks EncodeInt8's output back into float32 components.
func DecodeInt8(b []byte) []float32 {
	out := make([]float32, len(b))
	for i, x := range b {
		out[i] = float32(int8(x)) / int8Scale
	}
	return out
}
