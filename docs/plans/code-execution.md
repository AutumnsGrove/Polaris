# Sandboxed code execution

**Status: research/planning only — issue #42 asked for exactly that, not a design commitment.**
This doc answers #42's open questions with a recommendation, and covers #44 (code-generated charts)
as a follow-on section, since #44 explicitly depends on whatever ships here. No application code
has been written for either issue. Revised after a live discussion that corrected two things in
the first pass of this doc — see "What changed from the first draft" below.

## The constraints that decide almost everything else

Production runs on a Le Potato SBC: an Amlogic S905X, quad-core Cortex-A53 @ 1.5GHz, 1-2GB RAM
total, **and as of this writing only ~300MB of that is actually free** (confirmed by the person
running the box, not measured by this session). No other tool in this codebase runs
untrusted/generated code or spins up a second heavyweight runtime — every existing tool calls out
to a well-scoped external API (SearXNG, Brave, GitHub, Foursquare, ...). Arbitrary code execution
is a different risk *and* resource tier from anything already running here.

**Update 2026-09-13: live-verified against the real potato** via `ssh potato-remote` — see
"Blocking next step: RESOLVED" below for the actual measured numbers (real free RAM, real peak
RSS for the full numpy/pandas/matplotlib workload, real OOM floor). The rest of this doc's
security/mechanism reasoning was already sound without hardware access; only the memory question
below needed a live box, and it's now answered rather than hypothesized.

## What changed from the first draft

Two corrections came out of discussing this doc directly rather than shipping the first pass as
final:

1. **The original recommendation (a hosted API by default) was reaching for the wrong threat
   model.** "Arbitrary code execution needs strong isolation" is true for a multi-tenant service
   facing anonymous internet users — it's not automatically true for a single-operator tool behind
   Tailscale, where the code being run is written by a model *the owner* directed, not an
   adversarial stranger. Defaulting to an external paid service to solve a security problem this
   deployment doesn't actually have was the wrong call. Self-hosting on the potato, in Docker, is
   the right first target — not a fallback.
2. **The free-RAM number (~300MB) changes the resource math more than the CPU/virtualization
   question does.** The original draft focused on whether gVisor/Firecracker have a hardware-virt
   dependency; that's real, but it's not the binding constraint. A plain Python process importing
   pandas + numpy + matplotlib can plausibly land anywhere from ~150MB to 350MB+ RSS by import
   overhead alone (rough estimate, not measured — see "Blocking next step" below) — which may or
   may not fit next to whatever's already using the other ~1GB. This has to be measured on the real
   box before any library list or sandbox choice is finalized; it cannot be reasoned out from specs
   alone.

## Sandbox mechanism: plain locked-down Docker, not Piston, not gVisor/Firecracker

Compared three self-hosted shapes plus the hosted-API fallback:

| Approach | What it buys | What it costs | Verdict |
|---|---|---|---|
| **Plain locked-down Docker** (`--network none`, `--memory`/`--cpus`/`--pids-limit` caps, `--cap-drop=ALL`, `--read-only`, non-root, ephemeral per run) | Docker's own normal protections (seccomp default profile, capability restrictions, device isolation) stay fully active for the sandbox container — the exact same boundary every other container on this box already relies on. Nothing new to trust. | Slower per execution (full container start/stop) unless a warm pool is added later; you write your own resource-limit wrapper instead of getting one for free; only the languages/libraries you explicitly bake in. | **Recommended.** Proportionate to the actual threat model (one trusted operator, not adversarial multi-tenant traffic), and the isolation this hardware needs given no confirmed KVM support. |
| **Piston** (`engineer-man/piston`, uses `isolate` — namespaces/chroot/cgroups originally built for programming-contest judges) | Fast per-execution sandboxing (no container boot per call, just nested namespaces), dozens of languages out of the box, mature and widely used. | Requires `--privileged` + cgroup v2, specifically so `isolate` can create nested namespaces/cgroups from inside its own container. Per container-security literature, `--privileged` doesn't just add a capability — it removes seccomp filtering, capability restrictions, and device isolation *for that entire container*. Concretely: Judge0 (a different isolate-based system) had a symlink-path-validation bug turn into full **host root RCE** specifically because its container ran privileged — `isolate` itself worked as designed; the privileged blast radius is what turned an unrelated bug into a host compromise. | **Rejected.** The speed/language-breadth Piston buys aren't things this feature needs (Python only, low-frequency personal use) — the privilege escalation risk it costs is exactly the kind of thing that shouldn't sit next to a box holding personal data. Piston's own public instance (emkc.org) is also no longer open to the public as of Feb 2026 without special non-commercial-education authorization, so it's not available as a hosted alternative either. |
| **gVisor / Firecracker** | Strong isolation without Piston's privileged-container tradeoff (gVisor intercepts syscalls in userspace; Firecracker is a real VM boundary). | Depends on either KVM (`/dev/kvm` — unconfirmed on this board's Armbian setup) or gVisor's ptrace platform, which adds real per-syscall overhead on an already-weak quad-core A53. Both add real memory overhead of their own (a sentry process, or a full guest kernel per VM) on top of whatever the executed Python process itself needs. | **Not pursued for the potato specifically** — worth revisiting only if this ever runs on materially different hardware, or if a live check finds KVM is actually available and the memory math still works. |
| **Hosted API** (E2B, Modal) | Zero local memory/CPU footprint — execution happens entirely on someone else's infrastructure. Both have genuinely free tiers plausibly sufficient for personal-scale use: E2B's Hobby tier is a one-time $100 usage credit (no card required, ~$0.05/hour for a 1 vCPU sandbox after that — likely to last a very long time at occasional-use volume); Modal's free tier is $30/month in compute credits, recurring. | An external dependency and, eventually, a real (if small) recurring cost once free credit is exhausted — the thing this exploration was trying to avoid by default. | **Documented as the explicit fallback**, not the default — see below. |

## Blocking next step: RESOLVED — measured live on the real potato (2026-09-13)

Live-verified via `ssh potato-remote`, per CLAUDE.md's "verify on real hardware" rule, rather than
left as a hypothesis. Real numbers, not estimates:

- **Actual free RAM right now**: `free -h` shows 370MB truly free, 966MB "available" (includes
  reclaimable page cache) out of 1.9GB total — a materially better starting point than the
  ~300MB-free figure the first draft worked from (that number came from a verbal report, not a
  live read).
- **Built a real image**: `python:3.11-slim` (aarch64) + `pip install numpy pandas matplotlib`
  (numpy 2.4.6, pandas 3.0.5, matplotlib 3.11.2 — all had prebuilt aarch64 wheels, no source
  compilation needed on the weak quad-core A53). Image: 127MB compressed content, 577MB unpacked
  on disk. Disk is a non-issue — 205GB free on the potato's eMMC.
- **Ran the exact scenario the doc worried about** (import numpy+pandas+matplotlib, build a
  DataFrame, `savefig` a line chart) at shrinking `--memory` ceilings: succeeded all the way down
  to **80MB**; **64MB OOM-killed** (exit 137). Confirmed with real cgroup v2 accounting
  (`/sys/fs/cgroup/memory.peak` read from inside the container before exit, not an estimate):
  **peak RSS ~74MB** for the simple case.
- **Stress-tested with a heavier realistic workload** — 50,000-row DataFrame, `groupby`, NumPy
  RNG, a two-series line chart at 100dpi: **peak RSS ~82MB**. Even a materially bigger job than
  anything a chat-driven chart request would generate stayed under 85MB.

**This resolves the question the first draft couldn't answer from specs alone: the full library
set fits with enormous headroom**, not a tight squeeze. ~80MB peak against 370MB truly-free (and
966MB available) leaves 4-12x margin — comfortable even running alongside Polaris itself, SearXNG,
the Constellation scheduler, and backups without any of them needing to shrink first.

**Library set for v1, now decided**: **option 1, the full set** (numpy, pandas, matplotlib) —
matches Claude's own code-execution tool's baseline package set and ships #42 and #44 together.
No fallback to numpy-only or stdlib-only is needed; the memory math was never actually the
constraint it looked like on paper.

**A concrete `--memory` ceiling recommendation, informed by the measured floor**: cap the sandbox
container at **256MB** — over 3x the measured 82MB peak for a realistic workload (room for a
one-off heavier DataFrame or a busier plot without living dangerously close to the true ~74-82MB
floor), while still capping *far* below the ~370MB truly-free budget so a runaway/misbehaving
script can't come close to starving the host. `--pids-limit` and a wall-clock timeout in the Go
wrapper remain the other two legs of the resource-limit stool per "Other open questions" below.

**The hosted-API fallback (E2B/Modal) is no longer needed on capacity grounds** — it was only ever
justified by the memory math, and the memory math no longer supports it. Self-hosted plain Docker
is fully unblocked as the v1 implementation. (Re-priced Modal/E2B anyway per the "cheap first"
priority — see the new pricing note below, since the fallback is still worth knowing precisely
even though it's not being invoked.)

### Repricing the hosted fallback, since cost was the actual open question here

Re-researched 2026 pricing for the documented fallbacks (not needed for v1, but worth having
current numbers on file rather than the first draft's slightly dated figures):

| Platform | Free tier | Card upfront? | Realistic cost at personal-scale (10-30 execs/day) |
|---|---|---|---|
| **Modal.com** | $30/month *recurring* credit, no card | No | ~$0/month — dedicated `modal.Sandbox` API, $0.00003942/core-s + $0.00000672/GiB-s bills in cents at this volume |
| **E2B.dev** | $100 one-time credit **+ 100 sandbox-hours/mo** baseline, no card | No | ~$0/month — 100 free hrs/mo vastly exceeds a handful of few-second runs |
| **Daytona** | $200 one-time credit + 5GB storage, no card | No | ~$0/month, same order as E2B |
| Fly.io Machines | No real free tier since 2024 | Yes | ~$2-5/month floor even scaled to zero — worse than self-hosting |
| Cloudflare Sandbox/Containers | None; Workers Paid plan required | Yes | $5/month flat floor regardless of usage |

If self-hosting ever needs to be abandoned (hardware failure, a future memory-hungrier workload),
**Modal is the better-fit fallback of the two originally documented** — its credit recurs monthly
rather than being a one-time grant, and it has a sandbox-specific API rather than only a general
compute product. Not acted on now since it isn't needed.

### Package set, revisited: a broader "kitchen sink" set is affordable too (measured 2026-09-13)

Once the minimal set (numpy/pandas/matplotlib) was confirmed to fit with wide margin, the natural
follow-up question is whether the v1 set should be more generous — Python only pays import cost
(parse, C-extension load, RSS growth) for modules a script actually `import`s, so packages sitting
unused in `site-packages` cost disk, not RAM. Disk is a non-issue here (205GB free on the potato's
eMMC). Live-tested a broader candidate set closer to ChatGPT Code Interpreter's own package list
(this doc's sources): **numpy, pandas, matplotlib, scipy, scikit-learn, pillow, sympy, seaborn**,
with a script that actually imports and lightly exercises every one of them (not just import-and-
exit) — `scipy.stats.linregress`, `sklearn.linear_model.LinearRegression`, a `PIL.Image` draw+save,
a `sympy.integrate`, and a `seaborn.lineplot` savefig, all in the same process.

Results (real cgroup v2 `memory.peak`, same methodology as the minimal-set test):

| Memory ceiling | Result | Peak RSS |
|---|---|---|
| 512m / 384m / 300m / 256m | all succeeded | 200-263MB (run-to-run variance, no clear ceiling dependence) |
| 220m / 190m / 170m | all succeeded | 178-199MB |
| 150m | **OOM-killed** (exit 137) | — |

**Real floor is ~150-170MB** for the full eight-package set — noticeably higher than the minimal
set's ~74-82MB, as expected: `scikit-learn` pulls in its own `scipy`+`joblib`+`threadpoolctl`
stack, and `seaborn` layers on top of `matplotlib`+`pandas`+`scipy` again. Still comfortably inside
the ~370MB truly-free budget, but eating a much bigger fraction of it than the minimal set did —
this is a real tradeoff, not a free lunch: **broader packages are free until imported, but a
generated script that imports several of the heavy ones at once (scipy+sklearn+seaborn together)
will approach 200-260MB**, not the ~80MB the minimal set guaranteed.

**Recommendation**: ship the broader eight-package set (it's what makes something like Pillow
actually useful — see the fetch-tool discussion this informed), but raise the container `--memory`
ceiling recommendation from 256MB to **384MB** to keep a real safety margin (roughly 1.5-2x the
measured heavy-path peak, rather than sitting right at it) rather than the 3x+ margin the minimal
set allowed. One-time build cost for the broader set: **~8 minutes** on the potato's A53 (pip
resolving + downloading prebuilt aarch64 wheels for all eight packages) — paid once when the image
is built/rebuilt, not per execution.

**Two more formats worth accounting for, surfaced by the fetch-tool design** (see
`docs/plans/fetch-and-workspace-tools.md`): SQLite databases need no addition at all — Python's
`sqlite3` is stdlib, always present regardless of the package set decided above. Parquet needs
**`pyarrow`** added to the image — not yet in the measured eight-package set, but the same "free
until imported" logic applies, so it should be added alongside the others rather than treated as a
separate decision. ("Datasette" isn't a distinct file format worth its own handling — a Datasette
export is SQLite or JSON underneath, both already covered.)

## Deployment scope: Docker-only feature

Bare-metal installs have no container boundary at all for arbitrary code — running generated code
as a direct host subprocess is a real security downgrade with no equivalent mitigation available
without significant new work (rlimits/seccomp via something like bubblewrap, platform-specific and
still weaker than a container boundary). Code execution should **explicitly refuse under
bare-metal**, same pattern `cmd/install.go` already uses for "there's no equivalent under this
deployment model" rather than silently doing something less safe. A bare-metal `polaris` reports a
clear "code execution requires a Docker install" message instead of running anything.

## Other open questions, answered

- **Network access from executed code**: none, for v1. Nothing this issue asks for (data analysis,
  chart generation) needs outbound network from inside the sandbox, and removing it removes an
  entire exfiltration/SSRF concern for free. A real use case surfaced discussing this doc — Pillow
  is close to useless without some way to get a web-found image into the sandbox — but the answer
  isn't "give the sandbox a network"; it's a host-side, tool-mediated fetch (the fetch happens
  through the existing metered `web_search` pipeline, outside the sandbox, and only the resulting
  file is handed in) so the sandbox itself stays `--network none` permanently. Scoped out to its
  own design discussion — see #60.
- **Resource/time limits**: `docker run` flags (`--memory`, `--cpus`, `--pids-limit`) as the hard
  ceiling, plus a wall-clock timeout in the Go wrapper that kills the container if it overruns. A
  limit hit is a normal tool error back to the model ("your code didn't finish / used too much
  memory — simplify it or reduce the data size"), not a crash.
- **How results come back**: stdout/exit-status/error as the baseline structured result. A
  generated file (a chart image, specifically — see #44 below) reuses the existing attachment
  machinery (`gateway`'s attachments dir + `store.Store.SetMessageAttachment`) rather than a new
  storage path — save the returned image bytes as an attachment on the current message, render it
  the same way a user-uploaded image renders today.
- **Concurrency**: one *execution* at a time, globally, regardless of thread — there's no memory
  headroom on this box for two scripts actually running simultaneously, whichever threads they
  belong to. This is unrelated to (and doesn't block) the per-thread workspace persistence below —
  it only limits how many scripts can be *running* at once, not how many threads can *have* a
  workspace. A second `code_exec` call while one is in flight should queue or return a "busy" tool
  error rather than attempt to run alongside the first.

## File persistence: per-thread workspace, ephemeral containers (settled 2026-09-13)

Revised after discussing this doc directly: the container-per-execution model above is right for
*compute* (each `code_exec` call gets a fresh, thrown-away container — nothing about a Python
process's imports or variables needs to survive between calls, matching how Claude.ai's own code
interpreter behaves: a script that needs Pillow re-imports it fresh every time), but it's wrong for
*files* if left unqualified. The motivating case: turn 1 downloads a dataset and answers one
question about it; turn 10 asks a follow-up. Throwing the container away after turn 1 would mean
turn 10 has to re-download the same file — a real regression from how Claude.ai/ChatGPT's own code
interpreters behave, where a chat's files persist for the life of that chat.

**The resolved design**: every `code_exec` call stays a plain, fresh `docker run --rm` container
(no change from the design above) — but it's *always* bind-mounted to the same persistent,
host-side directory for that thread: `<workspace_root>/<thread_id>/`, next to `cfg.Attachments.Dir`
in the existing data layout (same UUID-per-entity convention already used there). A file fetched or
generated on turn 1 is just a file sitting in that directory; turn 10's call mounts the identical
directory and finds it already there. No re-download, no lost work, and no new container-lifecycle
machinery to build — persistence lives entirely in the directory, not in keeping any container
object alive. The tradeoff accepted deliberately: every call still pays the ~2s container-start
cost measured earlier, even for a thread that's been active for the last five minutes. **Future,
if that latency is ever actually a problem in practice**: a warmer path exists (keep one container
per actively-in-use thread running, `docker exec` into it instead of `docker run`-ing a new one,
stopping it after some idle window) — not built now, since it adds real lifecycle bookkeeping for a
latency cost nobody has reported minding yet.

**Uploaded attachments join the same workspace.** This supersedes `AttachmentData`'s current
behavior (see `tools/read_attachment.go`'s doc comment): today an uploaded PDF's bytes live only in
memory for the single turn it was uploaded on, never touching disk, gone the moment that turn ends.
Once a durable per-thread workspace exists for fetched files anyway, keeping uploads as the one
exception that *doesn't* persist stops being a deliberate safety choice and starts being an
inconsistency — an uploaded PDF should land in the same `<workspace_root>/<thread_id>/` directory
a fetched one would, so `read_attachment` can page through it on turn 10 exactly like a fetched
file, not just the turn it arrived on. See `docs/plans/fetch-and-workspace-tools.md` for how this
changes `read_attachment`'s own gating.

**Storage growth, and why it isn't an urgent problem**: the potato has 205GB free (measured
2026-09-13) against workspace files that are realistically small (datasets, a PDF, a generated
chart) — there's no pressure to build a cleanup mechanism now. **If storage ever does become a real
concern**, the cheap fix is a periodic sweep that deletes a thread's workspace directory after some
long inactivity window (30 days was the number discussed) — deliberately *not* paired with any
proactive "your files were cleared" notice injected into the thread. The simpler version: do
nothing special at flush time, and let the natural tool-error path handle it — `code_exec` or
`read_attachment` trying to reach a file that's gone just returns an ordinary "file not found, it
may have expired after a period of inactivity — re-fetch if needed" error, the same shape every
other tool failure in this codebase already surfaces to the model. No last-active-timestamp
tracking, no special first-message-after-flush detection required. Not building either version
now — noting it here so it isn't forgotten if disk usage ever actually needs attention.

## Follow-on: #44, code-generated charts

Rides whatever #42 ships rather than being its own sandboxing decision — becomes a question of
**how the result reaches the chat UI**, not a new mechanism:

- **Rendering path**: a code-generated chart comes back as an image (PNG, from matplotlib's own
  `savefig`), not structured `ChartSpec` JSON — it needs its own path in the chat UI, not
  `ChartCard.svelte`'s existing renderer. Simplest version: render it exactly like an image
  attachment (see "how results come back" above) — no new frontend component for a first version.
- **Does this replace `visualize`?** Not on day one. `visualize`'s fixed `ChartSpec` shape
  (`tools/visualize.go`) is cheap (no sandbox dependency, works even when code execution's memory
  budget is tight or the feature is disabled entirely under bare-metal) and already handles common
  cases well. Revisit once code execution is live and has real usage data — a decision this doc
  deliberately isn't making yet.
- **Directly gated by the memory test above — now unblocked.** The real hardware check landed on
  library option 1 (full set, matplotlib included) with wide margin, so #44 is no longer blocked
  on capacity and can ship alongside #42 rather than being deferred.

## Sources consulted

- Claude's code execution tool: Python 3.11, 1GB RAM, 5GB storage, preinstalled pandas/numpy/
  matplotlib — [Claude Platform Docs: Code execution tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool)
- ChatGPT Code Interpreter's broader default package set (scipy, sklearn, seaborn, plotly, PIL,
  opencv, nltk, ...) — [OpenAI Developer Community: Code Interpreter Python packages](https://community.openai.com/t/code-interpreter-python-packages-list-of-packages-available-in-the-environment/198477)
- Piston's mechanism, ARM64 availability, and public-instance status (closed to the public as of
  Feb 2026 without special authorization, 5 req/s even when granted) — [engineer-man/piston](https://github.com/engineer-man/piston), [piston readme.md](https://github.com/engineer-man/piston/blob/master/readme.md)
- Why `--privileged`/`CAP_SYS_ADMIN` removes seccomp/capability/device isolation for nested
  sandboxing — [Container Breakouts – Part 2: Privileged Container](https://blog.nody.cc/posts/container-breakouts-part2/), [Excessive Capabilities cheat sheet](https://0xn3va.gitbook.io/cheat-sheets/container/escaping/excessive-capabilities)
- The Judge0 symlink-write sandbox escape (isolate itself unaffected; privileged-container blast
  radius is what made it a host RCE) — [tantosec.com: Judge0 Sandbox Escape](https://tantosec.com/blog/judge0/)
- E2B Hobby tier and per-second pricing — [Morph: E2B Pricing Breakdown](https://www.morphllm.com/e2b-pricing), [Beam: E2B Pricing Explained](https://www.beam.cloud/blog/e2b-pricing-explained)
- Modal's free-tier compute credits — [Koyeb: Top Sandbox Platforms for AI Code Execution 2026](https://www.koyeb.com/blog/top-sandbox-code-execution-platforms-for-ai-code-execution-2026)
