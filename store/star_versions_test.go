package store

import (
	"testing"
)

func TestStar_UpdateStarSnapshotsVersionsAndReverts(t *testing.T) {
	s := openTestStore(t)
	id, err := s.CreateStar(Star{
		Title: "Cloudflare Workers", Category: "technology", Summary: "v0 summary",
		Body: "v0 body", Tags: []string{"edge"}, Confidence: "obvious", Status: "auto",
	})
	if err != nil {
		t.Fatalf("CreateStar: %v", err)
	}

	// A brand-new star has no versions yet — nothing has overwritten it.
	versions, err := s.GetStarVersions(id)
	if err != nil {
		t.Fatalf("GetStarVersions (before any update): %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("GetStarVersions (before any update) = %+v, want none", versions)
	}

	if err := s.UpdateStar(id, "", "v1 summary", "v1 body", []string{"edge", "workers"}, "fuzzy", nil, "weaver"); err != nil {
		t.Fatalf("UpdateStar (1st): %v", err)
	}
	if err := s.UpdateStar(id, "", "v2 summary", "v2 body", nil, "fuzzy", nil, "manual_edit"); err != nil {
		t.Fatalf("UpdateStar (2nd): %v", err)
	}

	versions, err = s.GetStarVersions(id)
	if err != nil {
		t.Fatalf("GetStarVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("GetStarVersions = %d entries, want 2", len(versions))
	}
	// Most recent first, and each entry captures what the star looked like
	// going INTO that update, not the result of it.
	if versions[0].VersionNumber != 2 || versions[0].Summary != "v1 summary" || versions[0].Source != "manual_edit" {
		t.Errorf("versions[0] = %+v, want version 2 snapshotting v1 summary, source manual_edit", versions[0])
	}
	if versions[1].VersionNumber != 1 || versions[1].Summary != "v0 summary" || versions[1].Source != "weaver" {
		t.Errorf("versions[1] = %+v, want version 1 snapshotting v0 summary, source weaver", versions[1])
	}

	// Reverting to version 1 (v0's content) must restore that content AND
	// itself create a new version 3 — a revert replays through UpdateStar
	// rather than rewriting history.
	if err := s.RevertStarToVersion(id, 1); err != nil {
		t.Fatalf("RevertStarToVersion: %v", err)
	}
	got, err := s.GetStar(id)
	if err != nil {
		t.Fatalf("GetStar (after revert): %v", err)
	}
	if got.Summary != "v0 summary" || got.Body != "v0 body" {
		t.Errorf("GetStar (after revert) = %+v, want v0 content restored", got)
	}

	versions, err = s.GetStarVersions(id)
	if err != nil {
		t.Fatalf("GetStarVersions (after revert): %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("GetStarVersions (after revert) = %d entries, want 3 (revert is append-only)", len(versions))
	}
	if versions[0].VersionNumber != 3 || versions[0].Summary != "v2 summary" || versions[0].Source != "revert" {
		t.Errorf("versions[0] (after revert) = %+v, want version 3 snapshotting v2 summary, source revert", versions[0])
	}

	if err := s.RevertStarToVersion(id, 999999); err != ErrStarVersionNotFound {
		t.Errorf("RevertStarToVersion(missing version) = %v, want ErrStarVersionNotFound", err)
	}
}
