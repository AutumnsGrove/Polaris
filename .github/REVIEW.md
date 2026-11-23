# Notes for the reviewer

Guidance for `pi-review`. Read the repository's own instructions first; this file says only where a
review's turns are worth spending.

## Spend them on running the thing

CI already runs what `go-ci.yml` and `frontend-ci.yml` run on every push and reports nowhere, so spend your turns on what a green run cannot
tell anyone.

1. **Reproduce the bug the pull request says it fixed.** The checkout is the head and it stays untouched:
   copy it elsewhere (`git archive HEAD | tar -x -C /tmp/polaris-revert`, or a `git worktree` under `/tmp`),
   revert the fix there, and run the test that is supposed to guard it. Put the failure in the comment. A
   regression test that passes against the code it was written to catch is the most common real defect
   there is.
2. **Run what the pull request claims to have run**, and compare its output with the figures in the body.
   A body that does not match the tree is a finding; a body that matches it is a reason to trust the rest.
3. **Derive the arithmetic** in any comment that asserts a count, a byte length, a boundary, a budget or a
   timeout. It is a claim you can check by computing it, and an off-by-a-few at an edge is the bug that
   survives both a compiler and a test suite written by the same person who wrote the bug.
4. **For every new test, delete the changed lines.** Does it still pass? If it does, the test is not
   testing the change, whatever its title says.
5. **For anything that decides pass or fail, read the exit code and the report shape it reads.** A script
   that reports success on a failed run is the highest-severity finding in a repository whose gate is the
   definition of done.

## Do not spend them on

Style, formatting and naming: the formatter owns those and runs in CI. And a finding you cannot point at
with `path:line`, or that no rule in this repository settles, is not a finding.

## Reporting

Post only what you ran, with the command and its output. If you could not reproduce something, say so in
one line, or drop it. Two demonstrated findings beat ten plausible ones, and "nothing found" is a good
review when it is true.

## What a green run does not show

`CLAUDE.md` says to verify on real hardware rather than by review or mocked tests. Say plainly which parts
of a change you could not check from the checkout — the container, the deployment, the hardware — instead
of treating a passing suite as evidence about them.
