# syntax=docker/dockerfile:1
#
# The code_exec sandbox image — NOT the main Polaris image (see the
# repo-root Dockerfile for that). Built and published to
# ghcr.io/autumnsgrove/polaris-sandbox by CI
# (.github/workflows/docker-publish-sandbox.yml), same reasoning as the
# main image's own GHCR publish: the potato's aarch64 quad-core A53 is
# far too weak to pay this build's cost (~8 minutes measured off-device
# — see docs/plans/code-execution.md's "Package set, revisited"), so
# install.sh's Docker path pulls the prebuilt image instead of building
# it locally. compose/watcher/codeexec.sh launches it as an ephemeral
# sibling container per code_exec call.
#
# Package set and memory ceiling are both measured on real hardware, not
# guessed — see docs/plans/code-execution.md's "Package set, revisited":
# real cgroup v2 peak RSS for this exact set landed at ~150-260MB on the
# potato's aarch64 Cortex-A53 across a light and a heavy realistic
# workload, comfortably under the 384MB --memory ceiling
# compose/watcher/codeexec.sh passes to `docker run` by default
# (config.Config.CodeExec.MemoryLimitMB).
FROM python:3.11-slim

RUN pip install --no-cache-dir \
        numpy pandas matplotlib scipy scikit-learn pillow sympy seaborn pyarrow && \
    find /usr/local/lib/python3.11 -name '__pycache__' -exec rm -rf {} + && \
    rm -rf /root/.cache

# A fixed non-root user, on top of (not instead of) the --cap-drop=ALL/
# --read-only/--network none/--pids-limit flags codeexec.sh sets at
# `docker run` time (not baked in here, so this image is still usable
# for a plain manual `docker run` while debugging) — the same "Docker's
# own normal protections stay fully active" posture
# docs/plans/code-execution.md chose over Piston's --privileged
# requirement or gVisor/Firecracker's unconfirmed-on-this-hardware
# virtualization dependency.
RUN useradd --no-create-home --uid 1000 sandbox

# --no-create-home means uid 1000 has no home directory at all — not
# just read-only, genuinely absent — so any library defaulting to
# $HOME/.something for cache/config (matplotlib's font/style cache is
# the one that actually surfaces, live-verified: a real sklearn+
# matplotlib code_exec run printed "mkdir -p failed for path
# /home/sandbox/.config/matplotlib: Read-only file system" to stderr on
# every single call despite still succeeding via its own fallback) hits
# this every time. Redirecting HOME to /tmp — the one writable location
# codeexec.sh's --tmpfs /tmp always provides regardless of the
# --read-only root filesystem — fixes it at the source instead of
# relying on each library's own fallback-and-warn behavior, which is
# needless stderr noise the model could otherwise misread as something
# having actually gone wrong. MPLCONFIGDIR is matplotlib's own more
# specific override, set alongside HOME rather than instead of it, in
# case some other library's fallback isn't as forgiving.
ENV HOME=/tmp
ENV MPLCONFIGDIR=/tmp/matplotlib

USER sandbox
WORKDIR /workspace
