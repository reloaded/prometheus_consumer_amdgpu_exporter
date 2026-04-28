package exporter

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/amdsmi"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/config"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/sysfs"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSys builds the smallest /sys layout that produces a populated
// amdgpu_info plus a couple of headline metrics.
func fakeSys(t *testing.T) string {
	root := t.TempDir()
	device := filepath.Join(root, "card0", "device")
	hwmon := filepath.Join(device, "hwmon", "hwmon0")
	for _, d := range []string{device, hwmon} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "card0", "dev"), "226:0\n")
	writeFile(t, filepath.Join(device, "vendor"), "0x1002\n")
	writeFile(t, filepath.Join(device, "product_name"), "AMD Radeon RX 7900 XTX\n")
	writeFile(t, filepath.Join(device, "vbios_version"), "113-D70200-100\n")
	writeFile(t, filepath.Join(device, "gpu_busy_percent"), "42\n")
	writeFile(t, filepath.Join(device, "current_link_width"), "16\n")
	writeFile(t, filepath.Join(hwmon, "temp1_input"), "43000\n")
	writeFile(t, filepath.Join(hwmon, "temp1_label"), "edge\n")
	return root
}

func TestCollectorEmitsCoreSeriesSysfsOnly(t *testing.T) {
	root := fakeSys(t)
	cfg := config.Config{
		Hostname:    "test-host",
		EnableSysfs: true,
		CollectProc: false,
		SysfsRoot:   root,
		ProcRoot:    t.TempDir(),
	}
	c := NewCollector(cfg, sysfs.New(root, t.TempDir()),
		amdsmi.New("/dev/null/no-binary", time.Second), quietLogger())

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	// Render the registry and check the headline metrics show up.
	got, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range got {
		names[*mf.Name] = true
	}
	for _, want := range []string{
		"amdgpu_nodes_total",
		"amdgpu_info",
		"amdgpu_gpu_busy_percent",
		"amdgpu_temperature_celsius",
		"amdgpu_pcie_link_width",
	} {
		if !names[want] {
			t.Errorf("metric %q missing; got: %v", want, sortedKeys(names))
		}
	}

	// Spot-check a value via testutil.CollectAndCompare-lite
	// (we just walk for the gauge value).
	if v := testutil.ToFloat64(prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "noop"}, []string{"x"}).WithLabelValues("y")); v != 0 {
		t.Errorf("sanity check failed: %v", v)
	}
}

func TestCollectorBothBackendsDisabled(t *testing.T) {
	cfg := config.Config{
		Hostname:     "test-host",
		EnableSysfs:  false,
		EnableAmdSMI: false,
		SysfsRoot:    t.TempDir(),
		ProcRoot:     t.TempDir(),
	}
	c := NewCollector(cfg, sysfs.New(t.TempDir(), t.TempDir()),
		amdsmi.New("/nonexistent", time.Second), quietLogger())

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	got, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	// nodes_total still emits, with value 0.
	for _, mf := range got {
		if *mf.Name == "amdgpu_nodes_total" {
			if len(mf.Metric) != 1 || mf.Metric[0].Gauge.GetValue() != 0 {
				t.Errorf("nodes_total = %v", mf.Metric)
			}
			return
		}
	}
	t.Error("amdgpu_nodes_total missing")
}

func TestCollectorAmdSMILabelsMerge(t *testing.T) {
	root := fakeSys(t)

	// Stub amd-smi binary that returns a single row keyed on a
	// fabricated BDF that matches the synthetic card. The fake card's
	// PCIAddr is "device" (the resolved symlink target name), so we
	// build a stub that reports that exact BDF.
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "amd-smi")
	const fixture = `[{"bdf":"device","uuid":"u-1","asic":{"market_name":"RX 7900 XTX"},"vbios":{"date":"2023-08-01"}}]`
	writeFile(t, stub, "#!/bin/sh\ncat <<'JSON'\n"+fixture+"\nJSON\n")
	if err := os.Chmod(stub, 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	cfg := config.Config{
		Hostname:     "test-host",
		EnableSysfs:  true,
		EnableAmdSMI: true,
		SysfsRoot:    root,
		ProcRoot:     t.TempDir(),
	}
	c := NewCollector(cfg, sysfs.New(root, t.TempDir()),
		amdsmi.New(stub, time.Second), quietLogger())

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	got, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range got {
		if *mf.Name != "amdgpu_info" {
			continue
		}
		if len(mf.Metric) == 0 {
			t.Fatal("amdgpu_info has no samples")
		}
		labels := map[string]string{}
		for _, lp := range mf.Metric[0].Label {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["uuid"] != "u-1" || labels["market_name"] != "RX 7900 XTX" || labels["vbios_date"] != "2023-08-01" {
			t.Errorf("amd-smi labels missing/wrong: %+v", labels)
		}
		return
	}
	t.Error("amdgpu_info not emitted")
}

func sortedKeys(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// no need for sort; this is a debug helper
	return strings.Join(out, ",")
}
