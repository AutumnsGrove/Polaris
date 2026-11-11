package embed

import (
	"math"
	"testing"
)

// TestEncodeInt8_GridValuesRoundTripExactly is the property the storage
// design depends on: pplx-embed emits values that are exact multiples of
// 1/128, so persisting int8 is lossless for the model Polaris actually
// indexes with. Verified against 300k+ real embedding components before
// int8 was chosen (see quant.go's doc comment).
func TestEncodeInt8_GridValuesRoundTripExactly(t *testing.T) {
	// Every representable step across the model's observed range.
	vec := make([]float32, 0, 241)
	for q := -125; q <= 119; q++ {
		vec = append(vec, float32(q)/int8Scale)
	}

	got := DecodeInt8(EncodeInt8(vec))
	if len(got) != len(vec) {
		t.Fatalf("decoded length = %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Fatalf("component %d = %v, want exactly %v", i, got[i], vec[i])
		}
	}
}

// TestEncodeInt8_OffGridValuesAreBounded covers a provider whose output is
// ordinary full-precision float (local nomic-embed-text). Quantization is
// lossy there, and the bound is what makes that acceptable.
func TestEncodeInt8_OffGridValuesAreBounded(t *testing.T) {
	vec := []float32{0.2371, -0.0998, 0.0003, -0.0007, 0.5, -0.5}
	got := DecodeInt8(EncodeInt8(vec))
	for i := range vec {
		if err := math.Abs(float64(got[i] - vec[i])); err > 1.0/(2*int8Scale)+1e-9 {
			t.Errorf("component %d error = %v, want <= 1/256 (~0.0039)", i, err)
		}
	}
}

func TestEncodeInt8_ClampsOutOfRange(t *testing.T) {
	// A provider emitting out-of-range values must saturate, not wrap: a
	// wrapped sign turns one large component into a wildly wrong one.
	got := DecodeInt8(EncodeInt8([]float32{3.0, -3.0}))
	if got[0] != float32(math.MaxInt8)/int8Scale {
		t.Errorf("above range = %v, want the positive clamp %v", got[0], float32(math.MaxInt8)/int8Scale)
	}
	if got[1] != -1 {
		t.Errorf("below range = %v, want exactly -1 (MinInt8/128)", got[1])
	}
}

func TestEncodeInt8_PreservesDimensionAndLength(t *testing.T) {
	b := EncodeInt8(make([]float32, 1024))
	if len(b) != 1024 {
		t.Fatalf("encoded length = %d, want 1024 (one byte per component)", len(b))
	}
	if len(DecodeInt8(b)) != 1024 {
		t.Errorf("decoded length = %d, want 1024", len(DecodeInt8(b)))
	}
}

func TestEncodeInt8_EmptyVector(t *testing.T) {
	if b := EncodeInt8(nil); len(b) != 0 {
		t.Errorf("encoding nil = %v, want empty", b)
	}
	if v := DecodeInt8(nil); len(v) != 0 {
		t.Errorf("decoding nil = %v, want empty", v)
	}
}

// TestEncodeInt8_PreservesRanking is the end-to-end property that matters:
// after a storage round-trip, a relevant vector still ranks above an
// irrelevant one with a comfortable margin.
func TestEncodeInt8_PreservesRanking(t *testing.T) {
	// Three vectors in the same space: query-ish, close, and far.
	near := make([]float32, 1024)
	far := make([]float32, 1024)
	for i := range near {
		near[i] = float32(math.Sin(float64(i))) * 0.2
		far[i] = float32(math.Cos(float64(i*7))) * 0.2
	}
	query := make([]float32, 1024)
	for i := range query {
		query[i] = near[i]*0.9 + float32(math.Sin(float64(i*3)))*0.02
	}

	beforeNear := CosineSimilarity(query, near)
	beforeFar := CosineSimilarity(query, far)
	afterNear := CosineSimilarity(query, DecodeInt8(EncodeInt8(near)))
	afterFar := CosineSimilarity(query, DecodeInt8(EncodeInt8(far)))

	if beforeNear <= beforeFar {
		t.Fatalf("test setup is wrong: near (%v) should outrank far (%v) unquantized", beforeNear, beforeFar)
	}
	if afterNear <= afterFar {
		t.Errorf("ranking inverted after quantization: near %v (was %v) vs far %v (was %v)",
			afterNear, beforeNear, afterFar, beforeFar)
	}
	// The margin should survive nearly intact — this is the number that
	// would show up if someone raised int8Scale or switched to a lossier
	// encoding.
	if delta := math.Abs(afterNear - beforeNear); delta > 0.01 {
		t.Errorf("near similarity drifted by %v after quantization, want < 0.01", delta)
	}
}

func FuzzEncodeInt8(f *testing.F) {
	f.Add(float32(0), float32(1), float32(-1), float32(0.5))
	f.Add(float32(1e9), float32(-1e9), float32(math.NaN()), float32(math.Inf(1)))

	f.Fuzz(func(t *testing.T, a, b, c, d float32) {
		vec := []float32{a, b, c, d}
		got := DecodeInt8(EncodeInt8(vec))
		if len(got) != len(vec) {
			t.Fatalf("round-trip changed length: %d -> %d", len(vec), len(got))
		}
		for i, v := range got {
			// NaN/Inf inputs are free to land on the clamp values; what
			// must never happen is a decoded value outside the encodable
			// range.
			if v < -1 || v > float32(math.MaxInt8)/int8Scale {
				t.Fatalf("component %d decoded to %v, outside the encodable range", i, v)
			}
		}
	})
}
