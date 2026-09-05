package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"polaris/store"
)

// validRoutineBody returns a create/update request body that
// validateSchedule accepts as-is — tests mutate a decoded copy of this to
// exercise one invalid field at a time.
func validRoutineBody() map[string]interface{} {
	return map[string]interface{}{
		"name":            "Daily tech news",
		"prompt":          "Summarize today's top tech news.",
		"model":           "mimo-pro",
		"focus_mode":      "",
		"deep_research":   false,
		"schedule_type":   "daily",
		"schedule_params": "",
		"time_of_day":     "07:00",
	}
}

func postPulsarRoutine(t *testing.T, h *testHarness, body map[string]interface{}) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(h.url("/api/pulsar/routines"), "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /api/pulsar/routines: %v", err)
	}
	return resp
}

func TestHandleCreatePulsarRoutine_HappyPath(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp := postPulsarRoutine(t, h, validRoutineBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var routine store.PulsarRoutine
	if err := json.NewDecoder(resp.Body).Decode(&routine); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if routine.ID == 0 {
		t.Error("ID = 0, want a real inserted id")
	}
	if routine.Name != "Daily tech news" || routine.ScheduleType != "daily" || routine.TimeOfDay != "07:00" {
		t.Errorf("routine = %+v, want the submitted fields echoed back", routine)
	}
	if routine.ArchivedAt != nil {
		t.Errorf("ArchivedAt = %v, want nil for a freshly created routine", routine.ArchivedAt)
	}
	if routine.LastRunAt != nil {
		t.Errorf("LastRunAt = %v, want nil for a routine that has never fired", routine.LastRunAt)
	}
}

// TestHandleCreatePulsarRoutine_RejectsInvalidSchedule tables through
// validateSchedule's rejection cases via the real HTTP path — the layer
// that had zero test coverage before this. Each case mutates exactly one
// field off an otherwise-valid body.
func TestHandleCreatePulsarRoutine_RejectsInvalidSchedule(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]interface{})
	}{
		{"empty name", func(b map[string]interface{}) { b["name"] = "  " }},
		{"empty prompt", func(b map[string]interface{}) { b["prompt"] = "" }},
		{"empty model", func(b map[string]interface{}) { b["model"] = "" }},
		{"bad time_of_day", func(b map[string]interface{}) { b["time_of_day"] = "7am" }},
		{"time_of_day out of range", func(b map[string]interface{}) { b["time_of_day"] = "25:00" }},
		{"unknown schedule_type", func(b map[string]interface{}) { b["schedule_type"] = "yearly" }},
		{"weekly missing weekday", func(b map[string]interface{}) {
			b["schedule_type"] = "weekly"
			b["schedule_params"] = ""
		}},
		{"weekly bad weekday", func(b map[string]interface{}) {
			b["schedule_type"] = "weekly"
			b["schedule_params"] = "someday"
		}},
		{"monthly missing day", func(b map[string]interface{}) {
			b["schedule_type"] = "monthly"
			b["schedule_params"] = ""
		}},
		{"monthly day zero", func(b map[string]interface{}) {
			b["schedule_type"] = "monthly"
			b["schedule_params"] = "0"
		}},
		{"monthly day too large", func(b map[string]interface{}) {
			b["schedule_type"] = "monthly"
			b["schedule_params"] = "32"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness(t, "http://127.0.0.1:1")
			body := validRoutineBody()
			tc.mutate(body)

			resp := postPulsarRoutine(t, h, body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for %s", resp.StatusCode, tc.name)
			}
		})
	}
}

// TestHandleCreatePulsarRoutine_AcceptsValidWeeklyAndMonthly locks in the
// two schedule kinds the table above only tests the rejection side of.
func TestHandleCreatePulsarRoutine_AcceptsValidWeeklyAndMonthly(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	weekly := validRoutineBody()
	weekly["schedule_type"] = "weekly"
	weekly["schedule_params"] = "Monday"
	resp := postPulsarRoutine(t, h, weekly)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("weekly: status = %d, want 200", resp.StatusCode)
	}

	monthly := validRoutineBody()
	monthly["schedule_type"] = "monthly"
	monthly["schedule_params"] = "15"
	resp = postPulsarRoutine(t, h, monthly)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("monthly: status = %d, want 200", resp.StatusCode)
	}
}

func TestHandleCreatePulsarRoutine_RejectsMalformedJSON(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Post(h.url("/api/pulsar/routines"), "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatalf("POST /api/pulsar/routines: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed JSON", resp.StatusCode)
	}
}

func TestHandleUpdatePulsarRoutine_HappyPath(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	createResp := postPulsarRoutine(t, h, validRoutineBody())
	var created store.PulsarRoutine
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	update := validRoutineBody()
	update["name"] = "Renamed routine"
	update["time_of_day"] = "08:30"
	b, _ := json.Marshal(update)
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/pulsar/routines/"+idString(created.ID)), bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var updated store.PulsarRoutine
	json.NewDecoder(resp.Body).Decode(&updated)
	if updated.Name != "Renamed routine" || updated.TimeOfDay != "08:30" {
		t.Errorf("updated = %+v, want the new name/time", updated)
	}
	if updated.ID != created.ID {
		t.Errorf("ID = %d, want unchanged %d across an edit", updated.ID, created.ID)
	}
}

func TestHandleUpdatePulsarRoutine_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	b, _ := json.Marshal(validRoutineBody())
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/pulsar/routines/999999"), bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a nonexistent routine", resp.StatusCode)
	}
}

func TestHandleUpdatePulsarRoutine_RejectsInvalidSchedule(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	createResp := postPulsarRoutine(t, h, validRoutineBody())
	var created store.PulsarRoutine
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	update := validRoutineBody()
	update["name"] = ""
	b, _ := json.Marshal(update)
	req, _ := http.NewRequest(http.MethodPatch, h.url("/api/pulsar/routines/"+idString(created.ID)), bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an invalid edit — the routine's own existing row must not have been touched", resp.StatusCode)
	}
}

func TestHandleArchivePulsarRoutine_HappyPath(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	createResp := postPulsarRoutine(t, h, validRoutineBody())
	var created store.PulsarRoutine
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	resp, err := http.Post(h.url("/api/pulsar/routines/"+idString(created.ID)+"/archive"), "application/json", nil)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	active, err := h.db.ListActivePulsarRoutines()
	if err != nil {
		t.Fatalf("ListActivePulsarRoutines: %v", err)
	}
	for _, r := range active {
		if r.ID == created.ID {
			t.Errorf("archived routine %d still appears in the active list", created.ID)
		}
	}
	archived, err := h.db.ListArchivedPulsarRoutines()
	if err != nil {
		t.Fatalf("ListArchivedPulsarRoutines: %v", err)
	}
	found := false
	for _, r := range archived {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("archived routine %d does not appear in the archived list", created.ID)
	}
}

// TestHandleArchivePulsarRoutine_Idempotent locks in ArchivePulsarRoutine's
// documented "archiving twice isn't meaningfully different from archiving
// once" behavior at the HTTP layer, not just in the store.
func TestHandleArchivePulsarRoutine_Idempotent(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	createResp := postPulsarRoutine(t, h, validRoutineBody())
	var created store.PulsarRoutine
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	for i := 0; i < 2; i++ {
		resp, err := http.Post(h.url("/api/pulsar/routines/"+idString(created.ID)+"/archive"), "application/json", nil)
		if err != nil {
			t.Fatalf("archive attempt %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("archive attempt %d: status = %d, want 204", i, resp.StatusCode)
		}
	}
}

func TestHandleArchivePulsarRoutine_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Post(h.url("/api/pulsar/routines/999999/archive"), "application/json", nil)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a nonexistent routine", resp.StatusCode)
	}
}

func TestHandleArchivePulsarRoutine_InvalidID(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Post(h.url("/api/pulsar/routines/not-a-number/archive"), "application/json", nil)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a non-numeric id", resp.StatusCode)
	}
}

func TestHandleUnarchivePulsarRoutine_HappyPath(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	createResp := postPulsarRoutine(t, h, validRoutineBody())
	var created store.PulsarRoutine
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	archiveResp, err := http.Post(h.url("/api/pulsar/routines/"+idString(created.ID)+"/archive"), "application/json", nil)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	archiveResp.Body.Close()

	resp, err := http.Post(h.url("/api/pulsar/routines/"+idString(created.ID)+"/unarchive"), "application/json", nil)
	if err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	active, err := h.db.ListActivePulsarRoutines()
	if err != nil {
		t.Fatalf("ListActivePulsarRoutines: %v", err)
	}
	found := false
	for _, r := range active {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("unarchived routine %d does not appear back in the active list", created.ID)
	}
}

func TestHandleUnarchivePulsarRoutine_NotFound(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Post(h.url("/api/pulsar/routines/999999/unarchive"), "application/json", nil)
	if err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a nonexistent routine", resp.StatusCode)
	}
}

func TestHandleListPulsarRoutines_ActiveAndArchivedAreSeparateLists(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	activeBody := validRoutineBody()
	activeBody["name"] = "Stays active"
	activeResp := postPulsarRoutine(t, h, activeBody)
	var active store.PulsarRoutine
	json.NewDecoder(activeResp.Body).Decode(&active)
	activeResp.Body.Close()

	archivedBody := validRoutineBody()
	archivedBody["name"] = "Gets archived"
	archivedResp := postPulsarRoutine(t, h, archivedBody)
	var archived store.PulsarRoutine
	json.NewDecoder(archivedResp.Body).Decode(&archived)
	archivedResp.Body.Close()

	arc, err := http.Post(h.url("/api/pulsar/routines/"+idString(archived.ID)+"/archive"), "application/json", nil)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	arc.Body.Close()

	resp, err := http.Get(h.url("/api/pulsar/routines"))
	if err != nil {
		t.Fatalf("GET routines: %v", err)
	}
	var activeList []store.PulsarRoutine
	json.NewDecoder(resp.Body).Decode(&activeList)
	resp.Body.Close()
	for _, r := range activeList {
		if r.ID == archived.ID {
			t.Error("the default (active) list includes an archived routine")
		}
	}

	resp, err = http.Get(h.url("/api/pulsar/routines?archived=true"))
	if err != nil {
		t.Fatalf("GET routines?archived=true: %v", err)
	}
	var archivedList []store.PulsarRoutine
	json.NewDecoder(resp.Body).Decode(&archivedList)
	resp.Body.Close()
	foundArchived, foundActive := false, false
	for _, r := range archivedList {
		if r.ID == archived.ID {
			foundArchived = true
		}
		if r.ID == active.ID {
			foundActive = true
		}
	}
	if !foundArchived {
		t.Error("?archived=true is missing the routine that was actually archived")
	}
	if foundActive {
		t.Error("?archived=true also returned a still-active routine")
	}
}

func TestHandleListPulsarPulses_EmptyForNewRoutine(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	createResp := postPulsarRoutine(t, h, validRoutineBody())
	var created store.PulsarRoutine
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	resp, err := http.Get(h.url("/api/pulsar/routines/" + idString(created.ID) + "/pulses"))
	if err != nil {
		t.Fatalf("GET pulses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var pulses []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&pulses); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(pulses) != 0 {
		t.Errorf("pulses = %+v, want none for a routine that has never fired", pulses)
	}
}

func TestHandlePulsarUnreadCounts_EmptyByDefault(t *testing.T) {
	h := newTestHarness(t, "http://127.0.0.1:1")

	resp, err := http.Get(h.url("/api/pulsar/unread"))
	if err != nil {
		t.Fatalf("GET unread: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var counts map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&counts); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("counts = %+v, want empty with no pulses fired yet", counts)
	}
}

func idString(id int64) string {
	return strconv.FormatInt(id, 10)
}
