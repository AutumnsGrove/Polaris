# Docker-only: converging on a single deployment model

**Status: designed, not yet implemented.** Filed against issue #61, which this doc **supersedes
the direction of** — #61 was opened 2026-09-13 (before code_exec was fully working) and its one
comment argued for the opposite conclusion (keep bare-metal primary, use Docker only narrowly as a
capability). Revisited 2026-09-14 with code_exec now real and durable: the decision is full
convergence, not the narrow middle path. #61's body/title should be rewritten to reflect this
rather than left as a stale "still deciding" issue — see "Tracking" below.

Second, dependent doc: `docs/plans/workspace-store-unification.md` — unifying attachments and
code_exec's per-thread workspace into one storage mechanism only becomes clean once every
deployment is guaranteed to have a workspace directory, which this doc is the prerequisite for.
That doc's full design is now decided (2026-09-15) — it's referenced here as the motivating "why
now," and its dev-loop testability requirement folds back into this doc's own "Dev loop" section
below, since the two are no longer independent asks.

## Why now (what changed since #61)

- Production has been Docker Compose only since 2026-08-15 (CLAUDE.md's "Production access")
  regardless of what the codebase still supports — bare-metal isn't what's actually running
  anywhere anymore, including for the only real user.
- code_exec (#42/#44) is the concrete proof that Docker-only features are here to stay, not a
  one-off exception bare-metal can keep refusing gracefully forever. Each new one (`tools/catalog.go`'s
  `docker_only` `Requires` case) widens the parity gap CLAUDE.md's dual-support checklist exists to
  manage.
- The Docker install path in `install.sh` is already mature — it self-installs Docker + the Compose
  plugin via `get.docker.com`, handles the docker-group re-login gotcha, verifies `docker compose`
  is present, waits for the daemon. "Make Docker easy to install" is not new work; going Docker-only
  is largely a deletion exercise on top of a path that already works.
- Only one real installer exists today (the owner, on the potato, already Docker). No external
  install base to protect — confirmed directly rather than assumed.

## Decisions

- **Full convergence for install/production, not the narrow middle path.** Every *deployed*
  instance is Docker; `install.sh` and production have no "bare-metal + optional Docker for
  capability X" branch to maintain.
- **Clean break, not staged deprecation.** Delete the bare-metal install/production code paths
  outright in this work rather than keeping a deprecated-but-working branch around for a
  transition window.
- **Local dev stays bare-metal for the Go backend, and `pnpm run dev` for the frontend — revised
  from this doc's first pass.** Go's `go build`/`go run` compile-and-restart loop is already fast;
  containerizing it would be a real regression for zero benefit, since `install.sh` never was part
  of a developer's inner loop in the first place (a developer edits source and runs the binary
  directly; `install.sh` is what an *end deployment* uses to stand up a fresh instance). Frontend
  dev stays exactly as it is today for the same reason — `DEVELOPMENT.md` is explicit that
  `web/build/` being committed exists because the *potato* can't run `pnpm`/`vite`, which has
  nothing to do with a developer's own machine. See "Dev loop: code_exec must still be fully
  testable, without containerizing Polaris" below for the one real design item this leaves.
- **Tracking**: #61 gets rewritten to reflect this direction (title + body), with a comment
  preserving *why* the original narrow-middle-path comment no longer applies (code_exec maturity),
  rather than silently overwritten. A second, separate issue is opened for
  `workspace-store-unification.md`, explicitly marked as depending on this one.

## What gets deleted

Confirmed by grep — every file that currently branches on `isDockerComposeInstall()` or
`gateway.deploymentMode()`:

- **`install.sh`** — the entire `bare-metal` branch (`INSTALL_MODE` conditionals throughout: the
  Go-toolchain install, `procmgr` systemd/launchd setup, the `POLARIS_INSTALL_MODE` env var itself
  since there's only one mode left).
- **`cmd/install.go`** — today this writes a systemd unit (Linux) or launchd plist (macOS) for
  bare-metal, and explicitly refuses under Docker ("there's no systemd/launchd unit for `polaris
  install` to set up here"). With bare-metal gone, the refusal becomes the *only* behavior — at
  that point `polaris install` as a command may not need to exist at all; Docker's own
  `restart: unless-stopped` already plays that role, and the update watcher's units are
  `install.sh`'s job already, not this command's. **Open item**: confirm whether to delete the
  command entirely or leave a short "not applicable under Docker" stub for anyone who still types
  it out of habit.
- **`procmgr/` (systemd.go, launchd.go)** — the whole package exists to serve `cmd/install.go`
  and `cmd/update.go`/`cmd/restart.go`'s bare-metal restart path. Dead once those callers are gone.
- **`cmd/docker_client.go`'s `isDockerComposeInstall()` gating** in `cmd/atlas.go`,
  `cmd/backup.go`, `cmd/constellation_backfill.go`, `cmd/restart.go`, `cmd/search.go`,
  `cmd/stats.go`, `cmd/update.go` — each currently branches "thin HTTP client to the container" vs.
  "direct local call." Only the Docker branch survives; the thin-client pattern becomes
  unconditional, not removed (it's still how a host-side CLI reaches a running container).
  **Decided**: this is a real, accepted behavior change for bare-metal dev, not an oversight — these
  commands go from "read `config.yaml`/`polaris.db` directly, cold, no server required" to "ask
  whatever Polaris server is listening on `localhost:8899`," which means a dev instance must
  actually be running (`go run .`) before `polaris stats`/`polaris backup`/etc. will work locally.
  Acceptable trade for deleting the duplicate code path. `runDockerModeCall`/`runDockerStats`-style
  helpers should give a clear, specific error when the connection fails — e.g. "no local Polaris
  server is reachable at localhost:8899; start one with `go run .` first" — rather than a bare
  connection-refused error, so the missing-server case is never confusing.
- **`gateway/update.go`'s `deploymentMode() == "bare-metal"` branch**, and `gateway/version.go`,
  `gateway/turn.go`, `gateway/settings.go`, `gateway/pulsar_daily.go` wherever they read
  `deploymentMode()` to branch behavior — each collapses to its Docker-only branch.
- **`tools/catalog.go`'s `docker_only` `Requires` case** and `tools/registry.go`'s
  `CodeExecEnabled` gating in `gateway/turn.go` — `code_exec`/`show`/`fetch_url`/`view_image`
  become unconditionally available; the whole "is this deployment Docker" check disappears from
  the tool-offering path.
- **`.githooks`, `.github/workflows/frontend-build-sync.yml`, the "commit `web/build/`" step** —
  this entire mechanism exists solely because bare-metal can't run `pnpm`/`vite build` itself.
  Docker always builds the frontend fresh from source (`Dockerfile`'s frontend stage). **Open
  item**: once nothing depends on a committed `web/build/`, decide whether to remove it from git
  and add it to `.gitignore`, or leave it committed as a convenience — leaning toward removing it,
  since a stale committed build with nothing checking it against source is worse than not having
  one.
- **CLAUDE.md's "This project ships two deployment models" section**, and the dual-path content in
  `SETUP.md`/`DEVELOPMENT.md`/`README.md` — rewritten to describe one model, once the code itself
  reflects it. Sequenced *after* code changes land, not before, so docs never describe a state that
  doesn't exist yet.

## What's unaffected

- **The host-side signal-file/watcher pattern** (`compose/watcher/`, `gateway/docker_update.go`,
  the code_exec sandbox handoff) — this exists because the *container* should never get Docker
  socket access, which has nothing to do with whether bare-metal exists as an alternative. No
  simplification available here; it's already the right shape.
- **GHCR multi-arch publishing** (`.github/workflows/docker-publish.yml`) — already automatic on
  every push to `main`. Nothing to build.
- **`main.Version`/`version.go`'s bare-string-literal constraint** — a `-ldflags -X` requirement
  independent of this decision; still applies.

## Dev loop: code_exec must still be fully testable, without containerizing Polaris

**Decided: this is required, not an open nice-to-have.** Confirmed directly with the owner —
`docs/plans/workspace-store-unification.md`'s design (file uploads becoming workspace files,
folding into `code_exec`'s storage) means the dev-loop wiring below now blocks testing *ordinary
file upload*, not just `code_exec` itself. It must be a fully figured-out, concrete setup before
any bare-metal deletion lands, not something worked out ad hoc later.

Not new infrastructure to build (no dev compose file, no `air`/`reflex` rebuild-on-save tooling) —
the real requirement is narrower: **code_exec's sandbox must be exercisable from a bare-metal dev
instance of Polaris**, since "test everything thoroughly before it lands on the potato" is a hard
requirement, not a nice-to-have, and relying on the potato itself for that testing is explicitly
rejected.

**The actual blocker, confirmed in the real code**: `gateway/turn.go`'s and
`gateway/pulsar_daily.go`'s `CodeExecEnabled` wiring both gate on
`deploymentMode() == "docker" && cfg.CodeExec.HostWorkspaceDir != ""`. `deploymentMode()`
(`gateway/version.go`) reads `POLARIS_DEPLOYMENT`, an env var **only ever set inside the Docker
container** (`docker-compose.yml`'s `polaris` service). This conflates two unrelated facts: "is
Polaris itself running inside a container" and "is a sandbox actually available to run generated
code in." The sandbox mechanism itself (`docker run --rm` for the executed Python, driven by the
host-side `compose/watcher/codeexec.sh` watching a signal directory) has never needed Polaris to be
*inside* a container — it only needs a reachable Docker daemon and a signal directory Polaris can
write requests into, both equally available on a dev machine as on the potato.

**Fix: make `CodeExecEnabled` a pure capability check, not a deployment-mode check.** Drop the
`deploymentMode() == "docker"` half of the condition entirely; keep (and possibly extend)
`cfg.CodeExec.HostWorkspaceDir != ""` (plus `SignalDir != ""`) as the sole gate. This is the same
reframing issue #61's original comment proposed for the whole install, just applied narrowly to
this one check instead of the entire deployment model — "needs Docker" becomes a capability gate,
not a deployment-mode gate, for code_exec specifically. `tools/catalog.go`'s `docker_only` label
stays unchanged (renaming it is more blast radius than it's worth for what's otherwise a purely
cosmetic mismatch now that it's really gating on "is code_exec configured").

**What dev actually needs, concretely** (none of it requires containerizing Polaris):
1. Docker installed on the dev machine (for the sandbox `docker run` only).
2. `compose/watcher/codeexec.sh` (or a dev-friendly equivalent — it doesn't need systemd, just to
   be running and watching the right directory) actually running, pointed at a local signal dir.
3. Dev's bare-metal `config.yaml` getting real `code_exec.workspace_dir`/`host_workspace_dir`/
   `signal_dir` values — today these only exist in the Docker-only
   `compose/polaris/config.yaml.example`; bare-metal's `config.yaml.example` needs the same fields
   with locally-appropriate paths.

**Open items**:
- Exactly how the watcher script runs in dev (a plain foreground terminal process is probably
  fine; doesn't need production's systemd path-unit rigor) — decide when actually wiring this up,
  not speculatively now.
- Audit every other `deploymentMode()` call site (`gateway/update.go`, `gateway/settings.go`,
  `gateway/pulsar_daily.go`) for the same conflation — code_exec's gate is the one confirmed and
  fixed here; others may or may not have the same issue and need their own look.

## Open items for implementation

- Decide `cmd/install.go`'s fate (delete vs. stub) — see above.
- Design the dev-loop compose shape concretely (its own short design pass, blocking before any
  code deletion actually lands, since the owner needs *something* to iterate with immediately).
- Audit every grep hit above individually rather than assuming each collapses identically — some
  (`cmd/search.go`/`cmd/stats.go`) have "ad hoc equivalents" per CLAUDE.md rather than the shared
  `runDockerModeCall` helper, so their collapse may look different file to file.
- Rewrite issue #61 (title + body + a comment explaining the reversal); open a new issue for
  `workspace-store-unification.md`, marked as depending on this one.
- Doc rewrite pass (CLAUDE.md, SETUP.md, DEVELOPMENT.md, README.md) — sequenced last.
