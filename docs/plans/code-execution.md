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

**I have not been able to live-verify any of this against the actual potato** — this session has
no SSH access to it. Per CLAUDE.md's "verify on real hardware, not just review" culture, treat
every claim below about what runs acceptably on this hardware as a hypothesis to confirm with a
real `ssh potato-remote` + resource-monitoring pass before writing implementation code, not as
settled fact. Where I'm not confident, I've said so rather than asserting it.

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

## Blocking next step: measure real memory on the potato before finalizing anything else

This can't be resolved from specs or general library-size estimates — it needs a real number from
the actual box. The concrete test:

```bash
ssh potato-remote
docker run --rm -m 256m python:3.11-slim python3 -c "
import numpy, pandas, matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
plt.plot([1,2],[1,2]); plt.savefig('/tmp/x.png')
print('ok')
" &
docker stats --no-stream
```

(Adjust `-m` and watch whether the process gets OOM-killed at various ceilings — 256m, 200m, 150m —
to find the real floor, and check `docker stats`' peak RSS for the container while it runs.) This
single test resolves the two things nothing else in this doc can:

- Whether pandas + numpy + matplotlib together fit in a fraction of ~300MB free RAM at all.
- If not, whether trimming to numpy + pandas only (no matplotlib, deferring #44) gets it under the
  ceiling instead.

Until this runs, **the library set for v1 is deliberately left undecided** rather than guessed —
three real candidates, in order of preference if the test allows it:

1. Full set (numpy, pandas, matplotlib) — matches Claude's own code-execution tool's baseline
   (Python 3.11, 1GB RAM, 5GB storage, preinstalled pandas/numpy/matplotlib — see sources). Ships
   #42 and #44 together.
2. numpy + pandas only, matplotlib (and #44) deferred until more headroom is measured or freed.
3. Stdlib only if even numpy+pandas doesn't comfortably fit — basic computation/data munging,
   revisit once there's more memory to work with.

If the real test shows even option 3 is too tight alongside everything else already running
(Polaris itself, SearXNG, the Constellation scheduler, backups), **E2B's Hobby tier or Modal's
monthly credits are the documented fallback** — not because the isolation argument favors them
(it doesn't, per the table above), but purely because they remove the memory question entirely by
running elsewhere. That's a materially different reason than the first draft's "arbitrary code
needs strong isolation" — worth being honest that this is a capacity decision, not a security one,
if it ends up being the one that's needed.

## Deployment scope: Docker-only feature

Bare-metal installs have no container boundary at all for arbitrary code — running generated code
as a direct host subprocess is a real security downgrade with no equivalent mitigation available
without significant new work (rlimits/seccomp via something like bubblewrap, platform-specific and
still weaker than a container boundary). Code execution should **explicitly refuse under
bare-metal**, same pattern `cmd/install.go` already uses for "there's no equivalent under this
deployment model" rather than silently doing something less safe. A bare-metal `polaris` reports a
clear "code execution requires a Docker install" message instead of running anything.

## Other open questions, answered

- **Network access from executed code**: none. Nothing this issue asks for (data analysis, chart
  generation) needs outbound network from inside the sandbox, and removing it removes an entire
  exfiltration/SSRF concern for free. A real use case for in-sandbox network access later would be
  a deliberate, separately-reviewed addition, not a default.
- **Resource/time limits**: `docker run` flags (`--memory`, `--cpus`, `--pids-limit`) as the hard
  ceiling, plus a wall-clock timeout in the Go wrapper that kills the container if it overruns. A
  limit hit is a normal tool error back to the model ("your code didn't finish / used too much
  memory — simplify it or reduce the data size"), not a crash.
- **How results come back**: stdout/exit-status/error as the baseline structured result. A
  generated file (a chart image, specifically — see #44 below) reuses the existing attachment
  machinery (`gateway`'s attachments dir + `store.Store.SetMessageAttachment`) rather than a new
  storage path — save the returned image bytes as an attachment on the current message, render it
  the same way a user-uploaded image renders today.
- **Concurrency**: one execution at a time. There's no memory headroom on this box for concurrent
  sandbox containers regardless of which library set the real test allows — a second `code_exec`
  call while one is in flight should queue or return a "busy" tool error rather than attempt to run
  alongside the first.

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
- **Directly gated by the memory test above**: if the real hardware check lands on library option
  2 or 3 (no matplotlib), #44 is blocked until more headroom exists, not silently descoped —
  worth its own explicit status update once the test runs, not a silent drop.

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
