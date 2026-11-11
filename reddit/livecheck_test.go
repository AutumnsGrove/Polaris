//go:build livecheck

// A real-network check of the read chain, because none of the unit tests above
// touch the actual archive — and the whole point of this package is that every
// other Reddit path stopped working. Skip it in CI (it needs network and the
// archive's goodwill); run it by hand after touching archive.go, or to answer
// "is Reddit still readable at all?":
//
//	go test -tags livecheck ./reddit/ -run TestLive -v
//
// If the thread case starts failing with "not in the archive", that's Reddit
// having turned the lights off rather than a Polaris bug — check
// https://status.arctic-shift.photon-reddit.com before changing code.
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
	c := NewClient("", "", "", "")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	res, err := c.Fetch(ctx, "https://www.reddit.com/r/golang/comments/1xt3k0b/")
	if err != nil {
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

func TestLiveListingAndUser(t *testing.T) {
	c := NewClient("", "", "", "")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	for _, url := range []string{
		"https://www.reddit.com/r/golang/top/?t=week",
		"https://www.reddit.com/u/spez",
	} {
		res, err := c.Fetch(ctx, url)
		if err != nil {
			t.Errorf("%s: %v", url, err)
			continue
		}
		t.Logf("%s -> %q (%d chars)", url, res.Title, len(res.Text))
		if !strings.Contains(res.Text, "https://www.reddit.com/r/") {
			t.Errorf("%s: listing has no thread links:\n%s", url, res.Text)
		}
	}
}
