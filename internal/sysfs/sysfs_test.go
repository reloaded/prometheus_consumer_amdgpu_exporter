package sysfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCard builds a synthetic /sys/class/drm/<name>/device tree on disk
// with the given file contents. Returns the discovered card.
func fakeCard(t *testing.T, root, name, vendor string, files map[string]string) {
	t.Helper()
	device := filepath.Join(root, name, "device")
	hwmon := filepath.Join(device, "hwmon", "hwmon0")
	if err := os.MkdirAll(hwmon, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "dev"), []byte("226:0\n"), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(device, "vendor"), []byte(vendor+"\n"), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(device, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	}
}

func TestDiscoverFiltersByVendor(t *testing.T) {
	root := t.TempDir()
	fakeCard(t, root, "card0", "0x1002", nil)
	fakeCard(t, root, "card1", "0x10de", nil) // NVIDIA — must not match

	r := New(root, t.TempDir())
	cards, err := r.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	if cards[0].Name != "card0" {
		t.Errorf("got card name %q, want card0", cards[0].Name)
	}
}

func TestReadCardPopulatesEverything(t *testing.T) {
	root := t.TempDir()
	fakeCard(t, root, "card0", "0x1002", map[string]string{
		"product_name":                      "AMD Radeon RX 7900 XTX",
		"vbios_version":                     "113-D70200-100",
		"revision":                          "0xc8",
		"gpu_busy_percent":                  "42",
		"mem_busy_percent":                  "17",
		"mem_info_vram_total":               "25769803776",
		"mem_info_vram_used":                "2147483648",
		"current_link_speed":                "16.0 GT/s PCIe",
		"max_link_speed":                    "16.0 GT/s PCIe",
		"current_link_width":                "16",
		"max_link_width":                    "16",
		"pp_dpm_sclk":                       "0: 500Mhz\n1: 2304Mhz *\n",
		"pp_dpm_mclk":                       "0: 96Mhz *\n1: 1325Mhz\n",
		"power_dpm_force_performance_level": "auto",
		"hwmon/hwmon0/temp1_input":          "43000",
		"hwmon/hwmon0/temp1_label":          "edge",
		"hwmon/hwmon0/temp2_input":          "48000",
		"hwmon/hwmon0/temp2_label":          "junction",
		"hwmon/hwmon0/power1_average":       "15000000",
		"hwmon/hwmon0/power1_cap":           "327000000",
		"hwmon/hwmon0/power1_cap_default":   "327000000",
		"hwmon/hwmon0/power1_cap_max":       "408000000",
		"hwmon/hwmon0/fan1_input":           "1234",
		"hwmon/hwmon0/pwm1":                 "64",
		"hwmon/hwmon0/in0_input":            "700",
	})
	r := New(root, t.TempDir())
	cards, err := r.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("got %d cards, want 1", len(cards))
	}
	snap := r.ReadCard(cards[0])

	if snap.Identity.Model != "AMD Radeon RX 7900 XTX" {
		t.Errorf("model = %q", snap.Identity.Model)
	}
	if snap.Identity.VBIOSVersion != "113-D70200-100" {
		t.Errorf("vbios = %q", snap.Identity.VBIOSVersion)
	}
	if snap.GPUBusyPct == nil || *snap.GPUBusyPct != 42 {
		t.Errorf("gpu_busy = %v", snap.GPUBusyPct)
	}
	if snap.LinkSpeedGTs == nil || *snap.LinkSpeedGTs != 16.0 {
		t.Errorf("link_speed = %v", snap.LinkSpeedGTs)
	}
	if snap.LinkWidth == nil || *snap.LinkWidth != 16 {
		t.Errorf("link_width = %v", snap.LinkWidth)
	}
	if snap.PerfLevel != "auto" {
		t.Errorf("perf_level = %q", snap.PerfLevel)
	}

	// Two clock domains, sclk active on 2304, mclk active on 96.
	clkByDomain := map[string]ClockReading{}
	for _, c := range snap.Clocks {
		clkByDomain[c.Domain] = c
	}
	if c, ok := clkByDomain["sclk"]; !ok {
		t.Fatal("sclk missing")
	} else if c.ActiveMHz == nil || *c.ActiveMHz != 2304 {
		t.Errorf("sclk active = %v", c.ActiveMHz)
	} else if c.MinMHz != 500 || c.MaxMHz != 2304 {
		t.Errorf("sclk range = %d..%d", c.MinMHz, c.MaxMHz)
	}
	if c, ok := clkByDomain["mclk"]; !ok {
		t.Fatal("mclk missing")
	} else if c.ActiveMHz == nil || *c.ActiveMHz != 96 {
		t.Errorf("mclk active = %v", c.ActiveMHz)
	}

	// Hwmon: two temperatures, power, power cap, one fan, one voltage.
	if len(snap.Hwmon.Temps) != 2 {
		t.Errorf("temps = %d, want 2", len(snap.Hwmon.Temps))
	}
	if snap.Hwmon.Temps[0].Sensor != "edge" || snap.Hwmon.Temps[0].Celsius != 43.0 {
		t.Errorf("temps[0] = %+v", snap.Hwmon.Temps[0])
	}
	if snap.Hwmon.PowerW == nil || *snap.Hwmon.PowerW != 15.0 {
		t.Errorf("power = %v", snap.Hwmon.PowerW)
	}
	if snap.Hwmon.PowerCapMaxW == nil || *snap.Hwmon.PowerCapMaxW != 408.0 {
		t.Errorf("power_cap_max = %v", snap.Hwmon.PowerCapMaxW)
	}
	if len(snap.Hwmon.Fans) != 1 {
		t.Fatalf("fans = %d, want 1", len(snap.Hwmon.Fans))
	}
	if snap.Hwmon.Fans[0].RPM == nil || *snap.Hwmon.Fans[0].RPM != 1234 {
		t.Errorf("fan rpm = %v", snap.Hwmon.Fans[0].RPM)
	}
	if snap.Hwmon.Fans[0].PWMPct == nil || *snap.Hwmon.Fans[0].PWMPct < 25 || *snap.Hwmon.Fans[0].PWMPct > 26 {
		t.Errorf("fan pwm pct = %v", snap.Hwmon.Fans[0].PWMPct)
	}
	if len(snap.Hwmon.Voltages) != 1 || snap.Hwmon.Voltages[0].Volts != 0.7 {
		t.Errorf("voltages = %+v", snap.Hwmon.Voltages)
	}
}

func TestDiscoverEmptyRoot(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "missing"), t.TempDir())
	cards, err := r.Discover()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(cards) != 0 {
		t.Errorf("got %d cards, want 0", len(cards))
	}
}

func TestFindRenderNodePrefersSysfsLink(t *testing.T) {
	root := t.TempDir()
	dri := t.TempDir()
	device := filepath.Join(root, "card3", "device")
	if err := os.MkdirAll(filepath.Join(device, "drm", "renderD131"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewWithDRI(root, t.TempDir(), dri)
	got := r.findRenderNode(Card{Name: "card3", DevicePath: device})
	want := filepath.Join(dri, "renderD131")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindRenderNodeFallsBackToCardNumber(t *testing.T) {
	dri := t.TempDir()
	r := NewWithDRI(t.TempDir(), t.TempDir(), dri)
	// Card with no drm/ subdirectory: fall back to renderD(128+N).
	got := r.findRenderNode(Card{Name: "card2", DevicePath: filepath.Join(t.TempDir(), "no-drm-dir")})
	want := filepath.Join(dri, "renderD130")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnsureWokenNoOpWhenRenderNodeMissing(t *testing.T) {
	// Point driRoot at an empty dir; ensureWoken should silently no-op,
	// not crash, when the render device file doesn't exist.
	dri := t.TempDir()
	r := NewWithDRI(t.TempDir(), t.TempDir(), dri)
	r.ensureWoken(Card{Name: "card7", DevicePath: filepath.Join(t.TempDir(), "card7")})
	if got := len(r.wakeFD); got != 0 {
		t.Errorf("wakeFD should stay empty when render node is missing, got %d entries", got)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestEnsureWokenCachesFDAcrossCalls(t *testing.T) {
	// Provide a real render node file; ensureWoken should open it
	// once and reuse the cached fd on subsequent calls.
	dri := t.TempDir()
	renderFile := filepath.Join(dri, "renderD128")
	if err := os.WriteFile(renderFile, []byte{}, 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	r := NewWithDRI(t.TempDir(), t.TempDir(), dri)
	c := Card{Name: "card0", DevicePath: filepath.Join(t.TempDir(), "card0")}
	r.ensureWoken(c)
	r.ensureWoken(c) // second call — should be a no-op
	if got := len(r.wakeFD); got != 1 {
		t.Errorf("wakeFD should hold 1 entry, got %d", got)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if got := len(r.wakeFD); got != 0 {
		t.Errorf("Close should drain wakeFD, got %d entries", got)
	}
}

func TestEnsureWokenPinsRuntimePMAndCloseRestores(t *testing.T) {
	// Synthesise a card whose power/control file we can read+write.
	// ensureWoken should write "on", remember the previous value,
	// and Close() should restore it.
	root := t.TempDir()
	cardName := "card0"
	device := filepath.Join(root, cardName, "device")
	powerDir := filepath.Join(device, "power")
	if err := os.MkdirAll(powerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctrlPath := filepath.Join(powerDir, "control")
	if err := os.WriteFile(ctrlPath, []byte("auto\n"), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}

	r := NewWithDRI(root, t.TempDir(), t.TempDir())
	c := Card{Name: cardName, DevicePath: device}
	r.ensureWoken(c)

	got, _ := os.ReadFile(ctrlPath)
	if strings.TrimSpace(string(got)) != "on" {
		t.Errorf("after ensureWoken, power/control = %q, want %q", string(got), "on")
	}
	if r.pinned[cardName] != "auto" {
		t.Errorf("pinned cache = %q, want %q", r.pinned[cardName], "auto")
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, _ = os.ReadFile(ctrlPath)
	if strings.TrimSpace(string(got)) != "auto" {
		t.Errorf("after Close, power/control = %q, want %q (restored)", string(got), "auto")
	}
	if len(r.pinned) != 0 {
		t.Errorf("Close should drain pinned cache, got %d entries", len(r.pinned))
	}
}
