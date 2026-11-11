package backup

import (
	"testing"
	"time"
)

func FuzzParseTime(f *testing.F) {
	f.Add("polaris-20260102-030405.db")
	f.Add("polaris-.db")
	f.Add("")
	f.Fuzz(func(t *testing.T, name string) {
		got, ok := parseTime(name)
		if ok && fileName(got) != name {
			t.Errorf("parseTime(%q) = %v, which names %q", name, got, fileName(got))
		}
	})
}

func FuzzFileNameRoundTrip(f *testing.F) {
	f.Add(int64(1700000000))
	f.Fuzz(func(t *testing.T, sec int64) {
		at := time.Unix(sec%253402300799, 0)
		if at.Year() < 0 || at.Year() > 9999 {
			t.Skip()
		}
		got, ok := parseTime(fileName(at))
		if !ok || !got.Equal(at.UTC().Truncate(time.Second)) {
			t.Errorf("round trip of %v gave %v, ok=%v", at, got, ok)
		}
	})
}
