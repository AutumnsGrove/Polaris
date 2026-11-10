# Archived: Polaris's bespoke agent loop

This is `agent/driver.go` as it stood at commit `ce32b39`, immediately before #188 deleted the
loop (`runBespoke`, `dispatchToolCallsConcurrently`, ~490 lines of the 1,071). Nova
(`github.com/AutumnsGrove/nova`) has been the only loop since. It is kept here for reference, not
for use: the `.txt` extension keeps it out of the build, and it will not compile against today's
tree (it predates the removal of `ForceOpenRouter`, the `nova_loop` flag and the harness picker).

Why it is safe to read rather than resurrect:

- Nova ran every production turn from 2026-10-27 to its removal with no rollback
  (`docs/plans/nova-loop.md`, "Production soak").
- What the old loop *produced* is not lost: `agent/testdata/nova_parity/*.json` are its recorded
  request bytes, events, transcripts and cost accounting for each parity scenario, and
  `agent/nova_parity_test.go` holds Nova to them.
- To run it again, check out `ce32b39` (or any earlier commit) and set `POLARIS_NOVA_LOOP=0`.
