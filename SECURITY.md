# Security

Polaris is a single-operator, self-hosted assistant, not a multi-tenant SaaS product — the threat
model here is "keep a model-directed agent, running unattended and reachable from your phone over
Tailscale, from ever becoming a way to compromise the host it runs on," not "protect user A's data
from user B." The boundaries below are the load-bearing ones; understanding them matters before
changing anything in `compose/watcher/`, `tools/code_exec.go`, or `gateway/docker_update.go`.

## The container never gets control over Docker

Polaris's own container has no Docker socket mount and no host filesystem access beyond its
bind-mounted data volume. This is deliberate: the one process in this system that makes
model-directed outbound requests and runs model-written code is the one process that must never be
able to reach the Docker daemon, `systemctl`, or arbitrary host files — a prompt-injected or
misbehaving turn should never be able to escalate past "answer a question badly."

Everything that needs host-level effects — pulling a new image, recreating the container,
restarting a unit, running generated code in a sandbox — goes through a **request/result file
handoff** instead: the container writes a small, fixed-shape JSON file into a bind-mounted signal
directory, and a host-side script (never invoked by the container itself) picks it up, acts on it,
and writes a result file back. See `gateway/docker_update.go` (`update-signal/`) and
`tools/code_exec.go` (`code-exec-signal/`, `docs/plans/code-execution.md`'s "How Polaris's own
container reaches Docker"). The model gets a narrow, reviewable capability — "here's some code, run
it with these resource limits" — never a live shell or socket.

## code_exec's sandbox lockdown

Model-written Python runs via `docker run --network none --cap-drop=ALL
--security-opt=no-new-privileges --read-only` plus a `--pids-limit`/`--memory` ceiling and a
`docker kill`-based watchdog that targets the daemon directly (not a CLI wrapper process — see
`compose/watcher/codeexec.sh`'s comment on why that distinction matters). Its only writable surface
is one explicit bind-mounted workspace directory. `codeExecGlobalLock`
(`tools/code_exec.go`) additionally limits this to one execution at a time process-wide, and
`agent/driver.go`'s tool dispatcher serializes multiple `code_exec` calls *within a batch* rather
than fanning them out like every other tool — not a security boundary by itself, but it removes the
fast-burst request-file pattern that once tripped systemd's start-limit on the host watcher (see
below).

## The update/codeexec watcher's own privilege boundary

`compose/watcher/update.sh` and `codeexec.sh` run as the operator's own unprivileged user (the
account that ran `install.sh`), not root — see `polaris-update.service`'s own comment on why.
They're deliberately narrow, reviewable scripts that each do one fixed thing (pull + recreate one
named container; run one fixed `docker run` invocation against a request Polaris itself wrote).

**The one exception, and the one place this needed real hardening:** `sync-units.sh` re-syncs the
watcher's own systemd unit files onto an already-installed host after a `git pull`, which requires
writing into `/etc/systemd/system/` and calling `systemctl` — genuinely root-only operations. If
the sudoers rule granting that access pointed straight at `sync-units.sh`'s path inside the git
checkout, **any commit reaching `main` — including a malicious one from a compromised contributor
account or token — would get root-executed on every production host the next time `polaris update`
ran, with zero human review.** That's a real supply-chain-to-privilege-escalation path.

The fix (issue #85): the sudoers rule (`/etc/sudoers.d/polaris-watcher`) grants NOPASSWD access
only to `/etc/polaris/watcher-sync-verify.sh` — a fixed, root-owned wrapper that `install.sh`
copies **outside** the git checkout, to a path `update.sh`'s automated `git pull` flow never
touches. Before running `sync-units.sh`, that wrapper checks the file's current SHA-256 against a
hash `install.sh` pinned into `/etc/polaris/watcher-sync.sha256` the last time a human actually ran
it. A hash mismatch — whether from a malicious commit or just an unreviewed legitimate change —
makes it refuse to run, non-fatally (the watcher units just keep whatever they already have until
the next successful sync). **Moving the pin forward requires re-running `install.sh`** — an action
no automated update cycle can trigger on its own, which is exactly what makes it a real approval
gate rather than security theater: if `update.sh` could re-approve its own pin, a malicious commit
could rewrite that logic in the same push.

Everything else `sync-units.sh` copies (`polaris-update.service`/`.path`/`.timer`,
`polaris-codeexec.service`/`.path`) doesn't need this same gate — those units all set
`User=@USER@`, so even tampered unit content only ever runs as the unprivileged deploy user, no
privilege gain over what that account could already do to itself.

If `compose/watcher/sync-units.sh` legitimately changes (a new feature, a bug fix), re-run
`install.sh` on the host to review the diff and re-approve it — see its own log output
("Approved the current compose/watcher/sync-units.sh...") for confirmation.

## Reporting a concern

This is a personal deployment, not a project accepting external security reports through a formal
program — but if you're self-hosting Polaris yourself and find a real issue in the boundaries
above, open a GitHub issue (or a private security advisory if it's actively exploitable) rather
than a normal PR, so it isn't publicly discussed before a fix ships.
