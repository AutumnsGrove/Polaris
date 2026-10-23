package store

import (
	"testing"
	"time"
)

func FuzzDecodeThreadCursor(f *testing.F) {
	f.Add(encodeThreadCursor(time.Unix(1700000000, 5), "abc"))
	f.Add("")
	f.Add("!!!")
	f.Fuzz(func(t *testing.T, cursor string) {
		ts, id, err := decodeThreadCursor(cursor)
		if err == nil {
			if _, perr := time.Parse(time.RFC3339Nano, ts); perr != nil {
				t.Errorf("accepted cursor %q with unparseable time %q", cursor, ts)
			}
			_ = id
		}
	})
}

func FuzzCursorRoundTrip(f *testing.F) {
	f.Add(int64(1700000000), int64(5), "abc")
	f.Add(int64(0), int64(0), "a|b|c")
	f.Fuzz(func(t *testing.T, sec, nsec int64, id string) {
		at := time.Unix(sec%253402300799, nsec%1e9) // keep within RFC3339's year range
		if at.Year() < 0 || at.Year() > 9999 {
			t.Skip()
		}
		gotTS, gotID, err := decodeThreadCursor(encodeThreadCursor(at, id))
		if err != nil {
			t.Fatalf("round trip failed: %v", err)
		}
		if gotID != id {
			t.Errorf("id = %q, want %q", gotID, id)
		}
		if parsed, _ := time.Parse(time.RFC3339Nano, gotTS); !parsed.Equal(at) {
			t.Errorf("time = %v, want %v", parsed, at)
		}
	})
}
