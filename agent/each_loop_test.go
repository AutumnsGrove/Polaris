package agent

import "testing"

// eachLoop runs body once on the bespoke loop and once on Nova, as named
// subtests. The loop is pinned through POLARIS_NOVA_LOOP rather than
// SetNovaLoop because the env var wins over the in-process switch: CI's
// second `go test` pass already exports it, and a test that only called
// SetNovaLoop would silently run both subtests on that one loop.
//
// Why this exists (#204): novaLoop defaults to false and nothing in this
// package's test binary turned it on, so the researcher tests only ever ran
// the bespoke loop, the one #188 deletes. The parity harness bypassed the flag
// by calling runWithNova/runBespoke directly; tests that go through Run (as
// RunSubAgent does) need this instead. Tests using it must not be parallel,
// since t.Setenv forbids it.
func eachLoop(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	for _, loop := range []struct{ name, env string }{{"bespoke", "0"}, {"nova", "1"}} {
		t.Run(loop.name, func(t *testing.T) {
			t.Setenv("POLARIS_NOVA_LOOP", loop.env)
			// Guard against the pin silently not taking effect.
			if got := novaLoopEnabled(); got != (loop.env == "1") {
				t.Fatalf("subtest %q resolved novaLoopEnabled()=%v", loop.name, got)
			}
			body(t)
		})
	}
}
