# Project: prometheus_consumer_amdgpu_exporter

Prometheus exporter for **consumer-grade AMD GPUs** (RDNA / RDNA2 /
RDNA3). Single static Go binary; published as a multi-arch container on
`ghcr.io/reloaded/prometheus_consumer_amdgpu_exporter`.

Why this exists: the upstream `rocm/device-metrics-exporter` is built
for AMD Instinct accelerators (MI200/MI300) and most of its series come
back empty/zeroed on a Radeon RX. This exporter reads from the kernel
sysfs surfaces consumer cards actually populate (and optionally shells
out to `amd-smi` for the few identity bits sysfs leaves to userspace).

## Commit style

- Do not add Co-Authored-By lines to commit messages.
- Keep commit messages concise: imperative mood, 1-2 sentence summary.

## Key documentation

- `README.md` — user-facing run + config docs
- `docs/worktrees.md` — git worktrees for concurrent and multi-machine development
- The motivating audit lives in the consumer (Ansible) repo at
  `docs/metrics/consumer-amdgpu-exporter-research.md`

## Language + layout

- Go ≥ 1.23, single module rooted at the repo
- Entrypoint: `cmd/prometheus_consumer_amdgpu_exporter/main.go`
- Library code under `internal/` (not importable from outside the module)
  - `internal/config` — env/flag-resolved runtime config
  - `internal/sysfs` — pure-Go sysfs / hwmon / fdinfo reader
  - `internal/amdsmi` — optional shell-out backend for `amd-smi`
  - `internal/exporter` — Prometheus collector that wires the backends
- Use `prometheus/client_golang` for metric registration; one
  `prometheus.Collector` for the whole exporter (single-target —
  there's only one host's GPUs per process).

## Test conventions

- `_test.go` next to the file under test
- Table-driven tests preferred
- sysfs tests use a synthetic on-disk layout in `t.TempDir()`; no
  `/sys` access from tests
- amd-smi tests stub the binary by pointing `AmdSMIPath` at a shell
  script committed under `testdata/`
- `make test` and `make lint` must pass before opening a PR; CI
  enforces this

## Lint / format

- `gofmt -s` (strict)
- `golangci-lint` with the linters in `.golangci.yml` — additions
  require a PR-level discussion
- No `nolint` comments without a one-line explanation

## Backends

The exporter has two backends, toggled independently via env vars (see
README for full list):

- **sysfs** (`CONSUMER_AMDGPU_EXPORTER_BACKEND_SYSFS`, default true).
  Reads `/sys/class/drm/card*/device`, the matching hwmon, and
  `/proc/<pid>/fdinfo`. Pure-Go, no cgo, no shell-outs. Covers fan
  RPM/PWM, link width, power cap, DPM steps, per-PID VRAM/engine.
- **amd-smi** (`CONSUMER_AMDGPU_EXPORTER_BACKEND_AMD_SMI`, default
  false). Shells out to `amd-smi static --json` and merges the
  identity payload (KFD UUID, VBIOS date, market name) into
  `amdgpu_info`. Best-effort: missing binary or parse errors log
  once and the backend stays out of the way. The same image is
  meant to be deployable on hosts with and without ROCm.

Both backends emit metrics with the `amdgpu_` prefix so this exporter
coexists with `rocm/device-metrics-exporter` (which uses `gpu_`).

## Permissions

The deploying side needs to grant:

- Read access to `/sys/class/drm` and the matching `hwmon` (no caps
  required; default container user can do this).
- For per-PID metrics: `CAP_SYS_PTRACE` or `hostPID=true` + root,
  so the exporter can read foreign PIDs' `/proc/<pid>/fdinfo`.
  Set `CONSUMER_AMDGPU_EXPORTER_COLLECT_PROCESSES=false` to
  disable the per-PID walk if the deployer can't grant either.

## Git workflow

- **NEVER commit directly to `main` or push to `main`. All work MUST be done on a feature branch. Absolute rule.**
- Create a `workitem/` feature branch (e.g. `workitem/sysfs-clocks`) before making any changes
- Each logical task → **one commit** containing all the changes from that task. No file-per-commit splatter
- When a task is complete:
  1. Stage all changed files from the task
  2. Commit to the feature branch with a concise message
  3. Push the commit to the remote branch
  4. Create a **draft** PR via `gh pr create --draft` if one doesn't exist
- Do not wait to be asked to commit/push — finish, commit, push, then move on
- **Always create PRs as drafts.** The user marks ready for review. Never flip draft state on subsequent pushes
- The repo is configured for **squash merges only** with **auto-delete of head branches on merge**. PR titles become the final commit message — write them well. Individual commits on the branch can be terse since they get squashed away
- Do not merge PRs — leave them for the user

## Release / image publishing

- Tags follow semver (`v0.1.0`, `v0.2.0-rc1`, …). Push a tag → release workflow runs
- Release workflow builds **multi-arch** images (linux/amd64 + linux/arm64) and publishes to `ghcr.io/reloaded/prometheus_consumer_amdgpu_exporter` with these tag aliases:
  - `:vX.Y.Z` (the semver tag)
  - `:vX.Y` and `:vX` (rolling minor/major)
  - `:latest` (only on non-prerelease tags)
  - `:sha-<short>` (the commit short SHA — useful for pinning during incident response)
- Pre-release tags (`-rc`, `-alpha`, `-beta`) skip `:latest` and the rolling minor/major aliases
- The `v0.1.0` tag on `main` is the legacy Python prototype; the Go rewrite re-baselines on `v0.2.0`. Do not delete the old tag — it's a useful reference for the audit history.

## Concurrent work with worktrees

Multiple Claude Code instances can work in parallel on separate tasks. Each instance **must** use a git worktree to avoid conflicts. See `docs/worktrees.md` for full details.

**At the start of every task**, use the `EnterWorktree` tool to create an isolated worktree for your branch.

- Two worktrees cannot have the same branch checked out simultaneously
- When the PR is merged (and the head branch auto-deleted), clean the worktree with `git worktree remove`

### Multi-machine workflow

Worktrees are local-only — they don't get pushed. To continue work on an existing remote branch from another machine:

1. `git fetch origin`
2. `git worktree add .claude/worktrees/<name> workitem/<branch>`
3. Pull before starting

If asked to continue work on an existing remote branch, check `git branch -r` first and create a worktree for it rather than starting a fresh branch.
