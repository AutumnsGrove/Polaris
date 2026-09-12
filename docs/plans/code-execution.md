# Sandboxed code execution

**Status: research/planning only — issue #42 asked for exactly that, not a design commitment.**
This doc answers #42's open questions with a recommendation, and covers #44 (code-generated charts)
as a follow-on section, since #44 explicitly depends on whatever ships here. No application code
has been written for either issue.

## The constraint that decides almost everything else

Production runs on a Le Potato SBC: an Amlogic S905X, quad-core Cortex-A53 @ 1.5GHz, 1-2GB RAM
(README.md: "too weak to run `pnpm install` + `vite build` in any reasonable time"; the Docker
image itself is 64MB specifically to stay light on this hardware). No other tool in this codebase
runs untrusted/generated code or spins up a second heavyweight runtime — every existing tool calls
out to a well-scoped external API (SearXNG, Brave, GitHub, Foursquare, ...). Arbitrary code
execution is a different risk *and* resource tier from anything already running here, and this
doc's recommendation is shaped by that, not by what would be the "purest" sandbox in the abstract.

**I have not been able to live-verify any of this against the actual potato** — this session has
no SSH access to it. Per CLAUDE.md's "verify on real hardware, not just review" culture, treat
every claim below about what runs acceptably on this hardware as a hypothesis to confirm with a
real `ssh potato-remote` + resource-monitoring pass before writing implementation code, not as
settled fact. Where I'm not confident, I've said so rather than asserting it.

## Option comparison

| Approach | Security isolation | Feasible on Le Potato? | Available libraries | Implementation cost |
|---|---|---|---|---|
| **Container (gVisor)** | Strong (syscall interception) | Uncertain — gVisor's `runsc` needs either KVM (hardware virt) or a ptrace platform; ptrace works without virt extensions but adds real per-syscall overhead on an already-weak quad-core A53. arm64 support exists but is less battle-tested than x86_64. | Full — real Python + pip | Moderate-high (a `runsc` install, a container image with the interpreter/libs baked in, resource cgroups) |
| **Container (gVisor)**, KVM platform | Strong | Needs `/dev/kvm` — Amlogic S905X has ARM virtualization extensions in silicon, but whether Armbian's kernel/bootloader config on this specific board exposes it is unconfirmed. If unavailable, falls back to ptrace above. | Full | Same as above, better perf if KVM is actually there |
| **microVM (Firecracker)** | Strong (real VM boundary) | Same KVM dependency as gVisor's KVM platform, with no ptrace fallback — if `/dev/kvm` isn't there, Firecracker is a non-starter, not just slower. Firecracker's own resource footprint (a full guest kernel per VM) is also heavier than this hardware has headroom for running more than one at a time. | Full (a real Linux guest) | High — Firecracker's own jailer, a minimal guest kernel/rootfs, VM lifecycle management from Go |
| **Locked-down plain Docker container** (no gVisor/Firecracker, just `--network none`, seccomp, resource limits, non-root) | Weak-moderate — shares the host kernel, so a container-escape CVE is a real host compromise, not "just" a sandbox breakout | Yes — this is what the rest of the Docker deployment already runs as, no new runtime dependency | Full | Low, but the security tier doesn't match "arbitrary model-influenced code" the way this issue's own framing wants |
| **WASM (Pyodide/WASI)** | Strong (WASM's own memory-safety sandbox, no syscalls unless explicitly granted via WASI) | Best fit for this hardware — no hardware virt dependency, no second container runtime, `wasmtime`/`wasmer`'s Go embeddings run as a library call from the existing Go binary. Real memory/CPU cost of a full Pyodide runtime (CPython compiled to WASM, tens of MB) is nontrivial on 1-2GB RAM but bounded and predictable, unlike a container's more variable overhead. | Limited to what's compiled for Pyodide's distribution — numpy/pandas/matplotlib all have official Pyodide builds (matters directly for #44), but not every PyPI package | Moderate — embedding wasmtime + Pyodide's runtime, wiring stdin/stdout/a virtual filesystem for output files |
| **Hosted code-execution API** (e.g. a managed sandbox service) | Strong, and it's someone else's operational problem | Trivially yes — zero local resource cost beyond an HTTP call, same shape as every other optional-API-key tool this codebase already has (Foursquare, GitHub token, Brave/Parallel/Tavily) | Whatever the service supports — typically full Python + common data/plotting libraries | Lowest by far — an HTTP client package (`codeexec/` following `tavily/`'s hand-rolled-client convention), a monthly-usage-cap closure exactly like `brave.MonthlyCap`/`parallelMonthlyCap` |

## Recommendation

**A hosted code-execution API, gated behind an optional config key, off by default** — not because
the sandboxing question is uninteresting, but because it's the only option that doesn't force a
choice between "weak isolation" and "resource cost this hardware plausibly can't sustain
alongside everything else `polaris` already does on it" (search, the agent loop, Constellation's
scheduler, Pulsar Daily's Stage A fan-out, backups). This matches an established pattern in this
codebase already: several tools (Foursquare, GitHub's higher rate limit, Brave/Parallel/Tavily)
are "optional, better/only available with a key," not core to the base install. Code execution
would join that list rather than becoming a new hard dependency of a base `polaris` install.

This doesn't rule out a self-hosted path later. If a live hardware check finds the potato has
more headroom than expected (or the household adds a second, beefier box), the WASM/Pyodide row
is the one worth revisiting first — it's the only self-hosted option that doesn't depend on
hardware virtualization support this board may not actually expose. Container/VM-based sandboxing
(gVisor, Firecracker) should be considered only if this ever runs on materially different
hardware; don't build toward them for the potato specifically.

Concretely: a small `codeexec` package (same hand-rolled-HTTP-client shape as `tavily/`), a
`code_execution.enabled` + `code_execution.api_key` config pair (unset means the tool isn't
offered at all — same `ctx.Tavily == nil` / `ctx.Brave == nil` pattern `tools/catalog.go`'s
`requires` gating already uses elsewhere), and a monthly usage cap closure through `store.Store`
matching `BraveUsageThisMonth`/`ParallelUsageThisMonth`'s shape if the chosen provider bills
per-call. The provider itself isn't picked here — that's its own quick survey (uptime, Python
library availability including matplotlib/pandas for #44, pricing, whether it returns generated
files or stdout-only) once this direction is confirmed, same spirit as the live OpenRouter
`/endpoints` surveys this file's sibling `models/models.go` entries already do before committing to
a provider.

## Open questions, answered

- **Network access from executed code**: none. A hosted sandbox call is already a single scoped
  network hop from Polaris's side; the code running inside it doesn't need its own network access
  for anything this issue actually asks for (data analysis, chart generation), and "no network
  inside the sandbox" removes an entire class of exfiltration/SSRF concern for free. If a real use
  case for in-sandbox network access shows up later, that's a deliberate, separately-reviewed
  addition, not a default.
- **Resource/time limits**: whatever the chosen hosted provider enforces natively (they all cap
  wall-clock and memory per execution) plus a client-side HTTP timeout on Polaris's side as a
  backstop, same shape as `runDockerConstellationBackfill`'s generous-but-bounded `http.Client`
  timeout. A timeout or provider-side limit hit is a normal tool error back to the model ("your
  code didn't finish in time — simplify it or reduce the data size"), not a crash.
- **How results come back**: structured JSON (stdout, exit status, any error) is the baseline
  return, same shape as every other tool's string result. A generated file (a chart image,
  specifically — see #44 below) should reuse the existing attachment machinery
  (`gateway`'s attachments dir + `store.Store.SetMessageAttachment`) rather than inventing a new
  storage path — save the returned image bytes as an attachment on the current message and let the
  frontend render it the same way a user-uploaded image renders today.
- **Realistically self-hostable on a Le Potato at all?** Only the WASM/Pyodide row is plausibly
  viable there today, and only after a real feasibility check (concurrent memory pressure against
  the rest of what's running); every container/VM-based option depends on hardware virtualization
  support this specific board's Armbian setup may not expose. Gating this whole feature behind an
  optional API key sidesteps needing that answer before shipping anything.

## Follow-on: #44, code-generated charts

Once code execution exists in the hosted-API shape above, letting the model write real
matplotlib/plotly code instead of only calling the fixed `visualize` tool (`tools/visualize.go`,
`ChartSpec` — line/bar/timeline/meter/range, enforced item caps like `visualizeMaxBars`) becomes a
question of **how the result reaches the chat UI**, not a new sandboxing decision — it rides
whatever #42 ships.

- **Rendering path**: a code-generated chart comes back as an image (PNG, most likely, from
  matplotlib's own `savefig`), not structured `ChartSpec` JSON — it needs its own path in the chat
  UI, not `ChartCard.svelte`'s existing renderer, which expects the fixed chart-kind shape. The
  simplest version: render it exactly like an image attachment (see "how results come back"
  above) — no new frontend component required for a first version, just the existing
  attachment-image rendering path.
- **Does this replace `visualize`?** Not on day one. `visualize`'s fixed `ChartSpec` shape is
  cheap (no extra network hop, no sandbox dependency, works even when code execution is disabled
  or its API budget is exhausted) and already handles the common cases well. Once code execution
  is live and has real usage data, that's the right time to revisit whether `visualize` still
  earns its keep as a separate tool or becomes a fallback for when code execution isn't
  configured — a decision this doc deliberately isn't making yet, per the original issue's own
  framing ("worth deliberately revisiting `visualize`'s scope... not just leaving both around
  indefinitely").
- **Library availability**: whichever hosted provider gets picked for #42 needs matplotlib (or an
  equivalent) confirmed available in its runtime — a real requirement to check during that
  provider survey, not an assumption.
