# Notes for the reviewer

Guidance for `pi-review`. Read the repository's own instructions first; this file says only where a
review's turns are worth spending.

## What this job can run

The job checks out `refs/pull/<n>/merge` — base merged into the head, not the head alone — and installs
dependencies. The Go module is installed and `go test` works. The frontend toolchain is **not**: the
lockfile lives under `web/`, and the installer only reads a repository-root lockfile, so `pnpm` and
`node_modules` are absent. `make check`-style commands that include a web or worker target will fail
here on a missing binary, which is the environment and never a finding.

## Spend them on running the thing

`go-ci.yml` runs only for `**/*.go`, `go.mod` and `go.sum`, and `frontend-ci.yml` only for `web/**`,
so a pull request touching neither — `prompts.yaml` alone is one — has **no CI run at all**, and
`prompts/prompts_test.go`'s drift check is exactly what nobody ran.

1. **Reproduce the bug the pull request says it fixed.** Work in a `git worktree add
/tmp/polaris-probe HEAD`;
   it carries `.git` and the history, so reverting the fix inside it works. `git archive` is the wrong
   tool: it carries no history and no ignored files, and its `tar -x -C` fails unless the directory exists.
   If the change touches a package that embeds a built frontend, build that in the copy first, or the
   revert comparison proves nothing.
2. **Run what the pull request claims to have run**, and compare its output with the figures in the body.
   A body that does not match the tree is a finding; a body that matches it is a reason to trust the rest.
   Keep it to the target and the package: the review step is capped at 20 minutes, and a review
   killed before it posts is worth nothing.
3. **Derive the arithmetic** in any comment that asserts a count, a byte length, a boundary, a budget or a
   timeout. It is a claim you can check by computing it, and an off-by-a-few at an edge is the bug that
   survives both a compiler and a test suite written by the person who wrote the bug.
4. **For every new test, delete the changed lines.** Does it still pass? If it does, the test is not
   testing the change, whatever its title says.
5. **For anything that decides pass or fail, read the exit code and the report shape it reads.** A script
   that reports success on a failed run is the highest-severity finding in a repository whose gate is the
   definition of done.

## Do not spend them on

Style, formatting and naming: the formatter owns those and the checks own the rest. And a finding you
cannot point at with `path:line`, or that no rule in this repository settles, is not a finding.

## Reporting

Post only what you ran, with the command and its output. If you could not reproduce something, say so in
one line, or drop it. Two demonstrated findings beat ten plausible ones, and "nothing found" is a good
review when it is true.
