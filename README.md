# prometheus_consumer_amdgpu_exporter

Prometheus exporter for **consumer-grade AMD GPUs** (RDNA / RDNA2 /
RDNA3). Single static Go binary; published as a multi-arch container on
`ghcr.io/reloaded/prometheus_consumer_amdgpu_exporter`.

> **Status:** Go rewrite landing on `workitem/go-rewrite`. The
> `v0.1.0` tag on `main` is the legacy Python prototype — kept around
> for archaeology but superseded by the Go code on this branch. The
> Go rewrite re-baselines on `v0.2.0`.

## Why another AMD GPU exporter?

The upstream [`rocm/device-metrics-exporter`](https://github.com/ROCm/device-metrics-exporter)
is built for AMD Instinct accelerators (MI200/MI300 — HBM, XGMI, ECC,
compute partitions). On a Radeon RX 7900 XTX the exporter reports
~40 ECC counters that are always zero, ~14 XGMI counters that don't
apply, plus `gpu_power_usage` and `gpu_energy_consumed` that are
broken on RDNA3. Many dashboard panels stay blank because the
metrics they query (`gpu_hbm_temperature`, `gpu_gfx_busy_instantaneous`,
`gpu_package_power`, `pcie_bandwidth`, …) are wrong-name or wrong-
hardware-class for consumer silicon.

This exporter is purpose-built for the consumer side of the line:

- Reads exactly what the kernel exposes for consumer cards
  (`/sys/class/drm/card*/device`, hwmon, `/proc/<pid>/fdinfo`).
- Emits *stable* metric names with `amdgpu_` prefix so it coexists
  with the Instinct exporter's `gpu_` series.
- Optionally pulls KFD UUID / VBIOS date / market name from
  `amd-smi` when ROCm is installed on the host.

In practice on a Radeon RX 7900 XTX you get useful series for
power draw, edge / junction / mem temperatures, fan RPM and PWM
duty, DPM-tracked clocks (sclk / mclk / fclk / socclk / vclk /
dclk / pcie), VRAM and GTT residency, PCIe link speed and width,
performance level, and per-process VRAM / engine busy time —
none of which the upstream exporter emits with non-zero values
on consumer silicon.

## Backends

Two collection backends, mix-and-match via env vars:

| Env var | Default | Notes |
|---|---|---|
| `CONSUMER_AMDGPU_EXPORTER_BACKEND_SYSFS` | `true` | Pure-Go reader for sysfs / hwmon / fdinfo. |
| `CONSUMER_AMDGPU_EXPORTER_BACKEND_AMD_SMI` | `false` | Shells out to `amd-smi static --json` for KFD UUID, VBIOS date, market name. Skipped silently if the binary is missing — same image works on hosts with and without ROCm. |
| `CONSUMER_AMDGPU_EXPORTER_AMD_SMI_PATH` | `amd-smi` | Path to the binary. Resolved through `$PATH` if unqualified. |
| `CONSUMER_AMDGPU_EXPORTER_AMD_SMI_TIMEOUT` | `5s` | Caps each `amd-smi` invocation. Go duration syntax (`5s`, `500ms`, …). |
| `CONSUMER_AMDGPU_EXPORTER_COLLECT_PROCESSES` | `true` | Walk `/proc/*/fdinfo` for per-PID series. Implied false when the sysfs backend is off. |
| `CONSUMER_AMDGPU_EXPORTER_LISTEN_ADDR` | `:9504` | HTTP listen address. |
| `CONSUMER_AMDGPU_EXPORTER_METRICS_PATH` | `/metrics` | Telemetry path. |
| `CONSUMER_AMDGPU_EXPORTER_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`. |
| `NODE_NAME` | `os.Hostname()` | Value used for the `instance` label. |

Truthy values: `1`, `true`, `yes`, `on`, `y`, `t` (case-insensitive).
Anything else is false.

CLI flags (`-web.listen-address`, `-web.telemetry-path`,
`-log-level`, `-amd-smi.path`, `-version`) override env vars when both
are set; see `--help` for the full list.

## Metrics

All metrics carry `gpu` (e.g. `card0`) and `instance` (hostname)
labels at minimum.

| Metric | Type | Extra labels | Source |
|---|---|---|---|
| `amdgpu_nodes_total` | gauge | — | discovery |
| `amdgpu_info` | info-gauge | card, pci_addr, model, vbios, revision, kernel, uuid, market_name, vbios_date | sysfs `product_name`, `vbios_version`, …; amd-smi `static --json` (last three) |
| `amdgpu_gpu_busy_percent` | gauge | — | `gpu_busy_percent` |
| `amdgpu_memory_busy_percent` | gauge | — | `mem_busy_percent` |
| `amdgpu_temperature_celsius` | gauge | sensor | `hwmon/temp{N}_input` |
| `amdgpu_power_usage_watts` | gauge | — | `hwmon/power1_average` |
| `amdgpu_power_cap_watts` / `_default_watts` / `_max_watts` | gauge | — | `hwmon/power1_cap*` |
| `amdgpu_fan_speed_rpm` | gauge | fan | `hwmon/fan{N}_input` |
| `amdgpu_fan_pwm` | gauge | fan | `hwmon/pwm{N}` |
| `amdgpu_fan_pwm_percent` | gauge | fan | derived |
| `amdgpu_voltage_volts` | gauge | rail | `hwmon/in{N}_input` |
| `amdgpu_clock_mhz` | gauge | domain | `pp_dpm_*` active step |
| `amdgpu_clock_min_mhz` / `_max_mhz` | gauge | domain | `pp_dpm_*` range |
| `amdgpu_vram_total_bytes` / `_used_bytes` | gauge | — | `mem_info_vram_*` |
| `amdgpu_vram_visible_total_bytes` / `_used_bytes` | gauge | — | `mem_info_vis_vram_*` |
| `amdgpu_gtt_total_bytes` / `_used_bytes` | gauge | — | `mem_info_gtt_*` |
| `amdgpu_pcie_link_speed_gts` / `_max_gts` | gauge | — | `current_link_speed`, `max_link_speed` |
| `amdgpu_pcie_link_width` / `_max` | gauge | — | `current_link_width`, `max_link_width` |
| `amdgpu_performance_level_info` | info-gauge | level | `power_dpm_force_performance_level` |
| `amdgpu_process_vram_bytes` | gauge | pid, comm | `/proc/<pid>/fdinfo/* drm-memory-vram` |
| `amdgpu_process_gtt_bytes` | gauge | pid, comm | `/proc/<pid>/fdinfo/* drm-memory-gtt` |
| `amdgpu_process_engine_busy_seconds_total` | counter | pid, comm, engine | `/proc/<pid>/fdinfo/* drm-engine-*` (use `rate()` in queries) |

## Running

### Container

```sh
docker run --rm \
  --pid=host \
  -v /sys:/sys:ro \
  --device /dev/dri \
  --group-add video --group-add render \
  -p 9504:9504 \
  ghcr.io/reloaded/prometheus_consumer_amdgpu_exporter:latest
curl -s localhost:9504/metrics | head
```

`--pid=host` and root-in-container are only needed for the per-PID
fdinfo walk. Set
`CONSUMER_AMDGPU_EXPORTER_COLLECT_PROCESSES=false` to drop those
series and run unprivileged.

### From source

```sh
make build
./bin/prometheus_consumer_amdgpu_exporter
```

The binary needs no special privileges to read `/sys/class/drm`; for
the per-PID metrics it needs `CAP_SYS_PTRACE` or to run as root.

## Development

- [`CLAUDE.md`](CLAUDE.md) — repo conventions (commits, branches, releases, tests, lint)
- [`docs/worktrees.md`](docs/worktrees.md) — git worktree workflow
- Layout follows `cmd/<bin>/main.go` + `internal/<pkg>` — each backend
  is its own internal package, the collector wires them together

```sh
make test     # go test -race -cover ./...
make lint     # golangci-lint
make fmt      # gofmt -s -w .
make image    # build a local Docker image
make build    # produce ./bin/prometheus_consumer_amdgpu_exporter
```

### Running against a synthetic sysfs

Both unit and integration tests use a fake `/sys/class/drm` tree
under `t.TempDir()` so they run on any host (no AMD GPU required).
For ad-hoc local testing without a GPU, point the exporter at a
fixture tree:

```sh
mkdir -p /tmp/fake-sys/card0/device/hwmon/hwmon0
echo '226:0'   > /tmp/fake-sys/card0/dev
echo '0x1002' > /tmp/fake-sys/card0/device/vendor
echo 'AMD Radeon RX 7900 XTX' > /tmp/fake-sys/card0/device/product_name
echo '42'    > /tmp/fake-sys/card0/device/gpu_busy_percent

CONSUMER_AMDGPU_EXPORTER_SYSFS_ROOT=/tmp/fake-sys \
CONSUMER_AMDGPU_EXPORTER_COLLECT_PROCESSES=false \
./bin/prometheus_consumer_amdgpu_exporter
```

`CONSUMER_AMDGPU_EXPORTER_SYSFS_ROOT` and `_PROC_ROOT` are escape
hatches for tests and local repro; production deployments don't
need to set them.

### Running against a stub `amd-smi`

amd-smi tests in `internal/amdsmi/` use a shell script under
`testdata/amd-smi-stub` that prints a fixture JSON document. To
exercise the merge logic ad-hoc:

```sh
cat > /tmp/amd-smi-stub <<'JSON'
#!/bin/sh
echo '[{"bdf":"0000:03:00.0","uuid":"abc-123","asic":{"market_name":"Radeon RX 7900 XTX"},"vbios":{"date":"2023-08-01"}}]'
JSON
chmod +x /tmp/amd-smi-stub

CONSUMER_AMDGPU_EXPORTER_BACKEND_AMD_SMI=true \
CONSUMER_AMDGPU_EXPORTER_AMD_SMI_PATH=/tmp/amd-smi-stub \
./bin/prometheus_consumer_amdgpu_exporter
```

You should see the `uuid`, `market_name`, and `vbios_date` labels
populate on `amdgpu_info`.

## Grafana dashboard

The companion dashboard lives at
[`reloaded/grafana-dashboards/consumer_amdgpu_exporter`](https://github.com/reloaded/grafana-dashboards/tree/main/consumer_amdgpu_exporter).

## License

[MIT](LICENSE)
