package benchmark

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// encryptForTest mirrors the Python encryption OpenAI used to produce the
// CSV (inverse of decrypt) so we can verify our decrypt is byte-exact
// without needing the real dataset.
func encryptForTest(plaintext, password string) string {
	sum := sha256.Sum256([]byte(password))
	digest := sum[:]
	key := make([]byte, len(plaintext))
	for i := range key {
		key[i] = digest[i%len(digest)]
	}
	out := make([]byte, len(plaintext))
	for i := 0; i < len(plaintext); i++ {
		out[i] = plaintext[i] ^ key[i]
	}
	return base64.StdEncoding.EncodeToString(out)
}

func TestDecryptRoundTrip(t *testing.T) {
	canary := "browsecomp:26b5c67b-test-canary"
	plaintext := "Plastic Man"
	ct := encryptForTest(plaintext, canary)
	got, err := decrypt(ct, canary)
	if err != nil {
		t.Fatalf("decrypt error: %v", err)
	}
	if got != plaintext {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestGrade(t *testing.T) {
	cases := []struct {
		resp    string
		correct bool
	}{
		{"reasoning: ...\ncorrect: yes\nconfidence: 100", true},
		{"reasoning: ...\ncorrect: no\nconfidence: 90", false},
		{"malformed garbage with no verdict", false},
	}
	for _, c := range cases {
		got, _ := Grade(c.resp)
		if got != c.correct {
			t.Errorf("Grade(%q) = %v, want %v", c.resp, got, c.correct)
		}
	}
}
