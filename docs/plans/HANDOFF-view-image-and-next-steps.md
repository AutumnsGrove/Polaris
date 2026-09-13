# Handoff: code_exec shipped, what's next

Scratch/status doc for picking this work back up — not a design doc itself (see the linked docs
below for that). Written 2026-09-13, updated same day after `code_exec` shipped and was
live-verified against a real model/DB (not yet against the real potato).

## What's actually shipped and working

### code_exec (commit `5e55f8f`, on `main`)

The sandboxed Python tool designed in `docs/plans/code-execution.md` (issue #42). Model-generated
Python runs in a locked-down, ephemeral Docker container (`--network none`, `--read-only`,
`--cap-drop=ALL`, memory/pids/time ceilings) launched by a host-side script
(`compose/watcher/codeexec.sh`) that Polaris's own container talks to via a plain request/result
JSON file handoff — the container itself never gets Docker socket access. Per-thread files persist
in a bind-mounted workspace directory across calls even though compute doesn't (a fresh container
every time). Docker-only; bare-metal explicitly refuses, and the tool is now hidden entirely from
the settings panel's toggle list on bare-metal (and Weaver's five `weaver_run` tools are hidden
there unconditionally) rather than showing an inert checkbox.

**Live-verified against a real OpenRouter model and the real dev `polaris.db`** (not just unit
tests, not the fake/scripted `dev/fakeopenrouter` path):

- Plain numpy (`np.std`) — correct result, correct DB event trail (`tool.code_exec` in `events`,
  both the exact code sent and the exact stdout received).
- scikit-learn `LinearRegression` + matplotlib `savefig` — a real 840×600 PNG landed in the
  thread's workspace directory on disk, fitted slope/intercept close to the true generative values.
- pandas DataFrame analysis (5 fictional products × 6 months) — correct per-product totals/averages
  in a real summary table.
- **The design-critical case**: three-turn cross-thread persistence in one thread — turn 1 builds a
  DataFrame, turn 2 saves it to `sales.csv` in the workspace, turn 3 (fresh container, told
  explicitly "do not regenerate the data") reads the file back cold and correctly answers a
  question against its real contents. Confirms the "ephemeral compute, persistent files" design
  actually behaves as designed, not just on paper.

**Four real bugs found only by running it, fixed before shipping** (`compose/watcher/codeexec.sh`):

1. `timeout --kill-after` wrapping `docker run` only kills the CLI process, not the container the
   daemon actually runs — confirmed live via `docker ps` showing a "timed out" container still
   `Up` tens of seconds later. Fixed with a named container (`--name codeexec-<id>`) and an explicit
   `docker kill` by name.
2. Under `set -e`, a bare `wait "$run_pid"` returning the job's own nonzero exit status kills the
   whole script before `exit_code` is ever assigned or a result file written — a correctly-detected
   timeout was silently lost, leaving Polaris to report a generic "no result" error instead.
3. `mktemp` creates its file immediately, so `[ -f "$watchdog_fired_file" ]` was true from the
   start regardless of whether the watchdog ever fired — misclassified a fast OOM kill (well under
   the timeout) as a timeout instead. Fixed with `mktemp -u` (path only, no file).
4. A multi-statement background subshell (`( sleep N; docker kill ...; ) &`) doesn't die when its
   wrapper PID is killed — the `sleep` survives as an orphan holding the script's own inherited
   flock lock fd, blocking every other pending `code_exec` call for up to the full timeout *after* a
   completely successful run. Fixed with `set -m` (job control) + `kill -- -"$pid"` (process-group
   kill) instead of a plain `kill "$pid"`.

**Not yet done**: live verification against the real potato under real Docker Compose + systemd
(today was deliberately local-only — a fresh local `config.yaml`/`polaris.db`, `POLARIS_DEPLOYMENT=
docker` forced by hand, the host watcher simulated with a polling loop since macOS has no systemd).
The actual systemd path-unit trigger (`polaris-codeexec.path`'s `DirectoryNotEmpty`), the real
Docker-outside-Docker networking on the potato's Armbian host, and `install.sh`'s new
`docker pull`/systemd-unit-install steps are all unexercised on real hardware. Per CLAUDE.md's
"verify on real hardware" rule, this should happen before calling the feature fully done — but
per direct instruction this session, **not yet**: keep testing local-only (temporary, easily
wiped) until told otherwise; don't touch the potato.

A security review of everything in commit `5e55f8f` is the very next thing queued up after this doc
update — see whichever review doc/findings landed alongside this one if you're resuming after that.

### view_image (commit `fd7cf0e`, on `main` — unchanged since last handoff)

Still shipped and live-verified as described in the previous version of this doc. See git history
for the original writeup if needed; not repeated here since nothing about it changed this session.

## What's designed but NOT built yet — this is the actual next work

In dependency order:

1. **`docs/plans/fetch-and-workspace-tools.md` (issue #60)** — `fetch_url` (provenance-scoped: a
   citation-checked URL or an `image_search` card index, content-type/size validated, host-side
   only) and `read_attachment`'s widened gating. **Was blocked on code_exec's workspace directory
   existing — now unblocked**, since `code_exec`'s per-thread `<workspace_root>/<thread_id>/`
   directory (config's `code_exec.workspace_dir`/`host_workspace_dir`) is exactly the directory
   `fetch_url` needs to write into. Nothing has been implemented yet.
2. **`view_image`'s `path` parameter** — already in the tool's schema (`tools/view_image.go`),
   currently always rejected with "viewing a workspace file isn't supported yet (code execution
   hasn't shipped)". **Also unblocked now** — wire `path` to read a file from
   `<workspace_root>/<thread_id>/<path>` the same way `card_index` reads from `ctx.CardsSnapshot()`
   today. Same describe/see mode logic applies unchanged, just a second image *source*. Small,
   self-contained — good candidate to do right after #60 or even before it.
3. **Chart rendering for code-generated images (`docs/plans/pulsar-daily.md`'s/#44's territory,
   and code-execution.md's own "Follow-on: #44" section)** — a real, live design idea surfaced
   mid-session worth acting on: **reuse the `highlight` tool's card-rendering machinery** instead of
   inventing new frontend plumbing for this. The actual gap (confirmed by an agent's research this
   session): an uploaded/generated attachment today only ever renders as a filename chip in the
   chat UI — the bytes are never re-served or shown inline. `highlight`'s `Card{Kind: "image", ...}`
   mechanism *does* already have a real frontend renderer (same one `image_search` uses). Serving a
   code_exec-generated file (a chart PNG, specifically) through that path — probably via a small new
   HTTP route that serves a workspace file by thread/filename as an image URL for a `Card` — avoids
   building genuinely new attachment-serving machinery from scratch. Not designed in detail yet;
   this paragraph is the starting point, not a finished plan.
4. Also still open, no urgency: **issue #61** (deprecate/unify bare-metal vs. Docker deployment
   modes) — a "someday" conversation, has a comment recording a concrete middle-path idea
   (bare-metal + an optional local Docker daemon used narrowly as a sandbox engine).

## Quick-start for resuming

- Read `docs/plans/code-execution.md` first if picking up #60 or the `view_image` path work — the
  "File persistence" section is exactly the workspace-directory contract both depend on.
- `docs/plans/fetch-and-workspace-tools.md` and `docs/plans/view-image.md` are the other two design
  docs from the original view_image session; `view-image.md` documents two real implementation
  decisions worth knowing (why `see` mode uses a synthetic follow-up message instead of putting the
  image in the tool result itself, why the multimodal gate lives in the handler rather than the
  tool schema).
- Issue #60 has a summary comment linking all three original design docs together.
- If resuming to finish `code_exec` itself rather than build on top of it: the potato
  live-verification pass (systemd units actually installed/triggering, real Docker-in-Docker
  networking, `install.sh`'s new steps exercised for real) is the one thing left unverified — do
  that with explicit go-ahead, not by default, per this session's instruction to stay local-only
  until told otherwise.
