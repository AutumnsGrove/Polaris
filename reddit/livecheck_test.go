//go:build livecheck

// A real-network check of the read chain, because none of the unit tests above
// touch the actual archive — and the whole point of this package is that every
// other Reddit path stopped working. Skip it in CI (it needs network and the
// archive's goodwill); run it by hand after touching archive.go, or to answer
// "is Reddit still readable at all?":
//
//	go test -tags livecheck ./reddit/ -run TestLive -v
//
// The cases are separate tests on purpose: run them one at a time with a pause
// between (see the note on the archive's shared window below).
//
// If the thread case starts failing with "not in the archive", that's Reddit
// having turned the lights off rather than a Polaris bug — check
// https://status.arctic-shift.photon-reddit.com before changing code. Running
// this twice within a minute can also get "arctic shift is overloaded": the
// archive's rate-limit window is shared between clients, so a listing query
// here closes it for the run after. Wait, then re-run; that's the archive
// being busy, not a regression.
package reddit

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLiveThread(t *testing.T) {
	// A permanently old, actively-commented thread: the archive has long since
	// settled it, so a failure here isn't a freshness race.
	c := NewClient("", "")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	res, err := c.Fetch(ctx, "https://www.reddit.com/r/golang/comments/1xt3k0b/")
	if err != nil {
		skipIfArchiveUnavailable(t, err)
		t.Fatalf("live archive read failed: %v", err)
	}
	t.Logf("source=%q title=%q", res.Source, res.Title)
	if res.Source != "arctic shift archive" {
		t.Errorf("source = %q, want the archive", res.Source)
	}
	if res.Title == "" || len(res.Text) < 200 {
		t.Errorf("thin result: title=%q text=%q", res.Title, res.Text)
	}
	if !strings.Contains(res.Text, "Comments:") {
		t.Errorf("no comment section:\n%s", res.Text)
	}
	excerpt := res.Text
	if len(excerpt) > 700 {
		excerpt = excerpt[:700] + "…"
	}
	t.Logf("rendered:\n%s", excerpt)
}

func TestLiveListing(t *testing.T) {
	c := NewClient("", "")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	res, err := c.Fetch(ctx, "https://www.reddit.com/r/golang/top/?t=week")
	if err != nil {
		skipIfArchiveUnavailable(t, err)
		t.Fatalf("live listing read failed: %v", err)
	}
	t.Logf("%q (%d chars)", res.Title, len(res.Text))
	if !strings.Contains(res.Text, "https://www.reddit.com/r/") {
		t.Errorf("listing has no thread links:\n%s", res.Text)
	}
}

func TestLiveUserPage(t *testing.T) {
	c := NewClient("", "")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	res, err := c.Fetch(ctx, "https://www.reddit.com/u/spez")
	if err != nil {
		skipIfArchiveUnavailable(t, err)
		t.Fatalf("live user-page read failed: %v", err)
	}
	t.Logf("%q (%d chars)", res.Title, len(res.Text))
	if !strings.Contains(res.Text, "https://www.reddit.com/r/") {
		t.Errorf("user page has no post links:\n%s", res.Text)
	}
}

// skipIfArchiveUnavailable turns the archive's own troubles into a skip: an
// "overloaded" reply (the expensive shapes, a user page especially, hit that
// whenever it is loaded) or a dropped connection (it does throttle a client
// that has been reading all afternoon). Neither says anything about whether
// Polaris can read Reddit, and failing on them would train whoever runs this to
// ignore the result.
func skipIfArchiveUnavailable(t *testing.T, err error) {
	t.Helper()
	for _, symptom := range []string{"overloaded", "unreachable"} {
		if strings.Contains(err.Error(), symptom) {
			t.Skipf("archive unavailable (%s), so this couldn't be verified: %v", symptom, err)
		}
	}
}
