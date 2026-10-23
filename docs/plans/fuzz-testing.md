# Fuzz testing — native `testing.F` for the untrusted-text parsers

**Added: 2026-10-23.**

**Status: in progress — Tranche 1 implemented (all five targets, plus `FuzzParse` and `FuzzExtractSearchQuery`) on `claude/fuzz-testing`; Tranche 2 implemented (7 of the 8 listed; `extractSearchQuery` moved into Tranche 1); Tranche 3 implemented (all 12 listed targets plus `FuzzBlocklistBlocked`, `FuzzCursorRoundTrip`, `FuzzExtractClaims`, `FuzzParseGitHubLastPage`, `FuzzParseGitHubSince`). Tranche 4 implemented (`.github/workflows/fuzz.yml`, manual + weekly). Extra targets beyond the plan: `uiblocks` (`FuzzSites`, `FuzzCloseBrackets`, `FuzzClip`), `eval` checkers, `backup.parseTime`, `threadexport` (stored-column parsers, `Build`+render).
Update this line as tranches land.**

## Why

Polaris is a single long-running binary that parses text it does not control: LLM output
(non-deterministic, often malformed) and web content (HTML, PDFs, URLs). A panic in any of those
parsers takes down the whole process and every in-flight turn. The existing suite is example-based
(177 test files, ~47 table-driven tests) and has no property or fuzz coverage, so it only exercises
inputs someone already thought of.

Go's built-in fuzzing (`func FuzzXxx(f *testing.F)`) is enough — no new dependency. Seed corpora run
as ordinary tests under `go test ./...`, so CI gets regression protection with no workflow change;
crashers found while fuzzing are saved to `testdata/fuzz/<Target>/` and replayed forever after.

Not worth fuzzing: regex backtracking (Go uses RE2, linear time), gateway/agent concurrency (race
tests and `dev/fakeopenrouter` own that), the frontend (`fast-check` + vitest if ever wanted).

## Conventions

- File name `fuzz_test.go` next to the code under test (same package, so unexported parsers are
  reachable).
- Name `FuzzXxx` after the function under test.
- Seed with representative real-world inputs, including the inputs from existing table tests.
- Every target asserts **no panic**, plus a cheap invariant where one exists (size caps, output
  charset, idempotence, round-trip). Don't write an oracle that re-implements the function.
- A crasher gets fixed in the code *and* gets a named regular regression test
  (`TestXxx_<Behavior>`), per `docs/STANDARDS.md`; the `testdata/fuzz` file is kept too.
- Run locally with `go test ./pkg -run=^$ -fuzz=FuzzXxx -fuzztime=60s`.

## Targets

### Tranche 1 — highest risk, pure functions

| Target | Where | Invariant |
|---|---|---|
| `FuzzParse` / `FuzzStrip` | `uiblocks/uiblocks.go` `parse`, `Strip` | no panic; `Strip` of content with no ui fence is identity |
| `FuzzExtractPDFPage` | `tools/web_read.go` | no panic on arbitrary bytes; `totalPages >= 0`; error implies empty text |
| `FuzzParsePseudoToolCalls` | `agent/pseudocall.go` | no panic; every returned call has non-empty name and valid JSON args |
| `FuzzExtractJSONBlock` | `tools/subagent_report.go` | no panic; non-empty result starts with `{` and ends with `}` |
| `FuzzSplitIntoSpeechChunks` | `voice/chunk.go` | no panic; no empty chunks; chunks only exceed the cap if a single sentence does |

### Tranche 2 — LLM-output parsers

`gateway.parseReconcileResponse`, `gateway.parseDailyVerdict`, `gateway.parsePulsarSuggestion`,
`tools.ParseSubAgentReport`, `oraclebench.parseJudgment`, `agent.extractSearchQuery`,
`gateway.sanitizeGeneratedTitle`, `tools.parseTavilyYouTubeTranscript`.

### Tranche 3 — identifiers, URLs, cursors, chunking

`tools.parseGitHubRepo`, `tools.extractYouTubeID`, `tools.parseGitHubSince`,
`tools.parseGitHubLastPage`, `search.normalizeDomain`, `store.decodeThreadCursor`,
`llm.DecodeTranscript`, `gateway.splitIntoChunks`, `gateway.normalizeForQuote`,
`gateway.extractClaims`, `tools.stripBoilerplateLines`, `voice.StripMarkdown`.

### Tranche 4 (optional) — CI

A manual/scheduled `fuzz.yml` workflow running each target for ~60s, kept out of the PR path so it
can't slow merges.

## Findings log

Fill in as targets are run: target, input (or `testdata/fuzz` file), root cause, fix commit.

| Target | Input | Root cause | Fix |
|---|---|---|---|
| `FuzzExtractPDFPage` | `tools/testdata/fuzz/FuzzExtractPDFPage/29c386a38e140cad` (truncated xref, bogus `startxref`) | `ledongthuc/pdf` panics on malformed input instead of returning an error; would crash any caller not under the agent driver's per-tool `recover` | `pdfPageText` recovers and returns an error; `TestExtractPDFPage_MalformedPDF_ReturnsErrorInsteadOfPanicking` |
| `FuzzSanitizeGeneratedTitle` | `gateway/testdata/fuzz/FuzzSanitizeGeneratedTitle/bfd3c63a68147d0b` | `title[:maxTitleLen]` cut mid-rune, storing invalid UTF-8 for CJK/accented titles | cut backs off to a rune boundary; `TestSanitizeGeneratedTitle_TruncatesOnRuneBoundary` |
| `FuzzSplitIntoChunks` | seed `strings.Repeat(strings.Repeat("é",700)+"x\n\n",40)` (the fuzzer's own mutations never reached this: chunking needs tens of KB of input, so it was found by reading the code, then added as a seed) | the chunk-overlap byte offset in `splitIntoChunks` could land mid-rune, starting the next chunk with stray continuation bytes | overlap start advances to a rune boundary; `TestSplitIntoChunks_OverlapNeverStartsMidRune` |
| `FuzzParseTime` | `backup/testdata/fuzz/FuzzParseTime/16ec8989d04ea382` (`polaris-00000101-000000,0.db`) | `time.Parse` tolerates a fractional-seconds suffix the layout lacks, so a non-Polaris file parsed as a backup (and could be listed/pruned as one) | `parseTime` requires `fileName(t) == name`; `TestParseTime_RejectsNonCanonicalNames` |

Lesson: a fuzzer only finds what its inputs can reach. For size-gated code (chunking, caps), seed an input past the threshold by hand.
