// Package sysfs reads consumer AMD GPU state from the kernel sysfs
// surfaces (/sys/class/drm/card*/device, the matching hwmon, and
// /proc/<pid>/fdinfo). Pure-Go, no cgo, no shell-outs — the package
// only depends on the standard library.
package sysfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRoot is the sysfs DRM root path; overridable for tests.
	DefaultRoot = "/sys/class/drm"
	// DefaultProc is the procfs root path; overridable for tests.
	DefaultProc = "/proc"

	// amdVendorID is the value of /sys/class/drm/card*/device/vendor for
	// AMD GPUs.
	amdVendorID = "0x1002"
)

// Reader is the entrypoint into the sysfs backend. It is safe for
// concurrent use; nothing is cached across scrapes (sysfs reads are
// cheap and we want each /metrics call to reflect the current state).
type Reader struct {
	root     string
	procRoot string
	driRoot  string
}

// New returns a Reader rooted at the given paths. Pass DefaultRoot /
// DefaultProc for production use.
func New(root, procRoot string) *Reader {
	if root == "" {
		root = DefaultRoot
	}
	if procRoot == "" {
		procRoot = DefaultProc
	}
	return &Reader{root: root, procRoot: procRoot, driRoot: "/dev/dri"}
}

// NewWithDRI is like New but lets tests point /dev/dri at a tempdir.
func NewWithDRI(root, procRoot, driRoot string) *Reader {
	r := New(root, procRoot)
	if driRoot != "" {
		r.driRoot = driRoot
	}
	return r
}

// Card describes one discovered AMD GPU.
type Card struct {
	// Name is the DRM card identifier as it appears in sysfs (e.g. "card0").
	// This is also what the exporter emits as the `gpu` label so a host
	// with multiple AMD GPUs gets stable per-card series.
	Name string
	// DevicePath is the absolute path to the card's device directory
	// (e.g. /sys/class/drm/card0/device).
	DevicePath string
	// HwmonPath is the absolute path to the matching hwmonN directory,
	// or "" if the kernel hasn't exposed one (rare; only on driver-load
	// races).
	HwmonPath string
	// PCIAddr is the PCI bus address (e.g. "0000:03:00.0").
	PCIAddr string
}

// Discover walks sysfs and returns every AMD GPU card on the host.
func (r *Reader) Discover() ([]Card, error) {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read drm root %q: %w", r.root, err)
	}

	var cards []Card
	for _, e := range entries {
		name := e.Name()
		if !cardNameRE.MatchString(name) {
			continue
		}
		device := filepath.Join(r.root, name, "device")
		vendor, _ := readText(filepath.Join(device, "vendor"))
		if vendor != amdVendorID {
			continue
		}

		// Resolve `device` (a symlink) to find the PCI BDF.
		pciAddr := ""
		if target, err := os.Readlink(device); err == nil {
			pciAddr = filepath.Base(target)
		} else if abs, err := filepath.EvalSymlinks(device); err == nil {
			pciAddr = filepath.Base(abs)
		}

		cards = append(cards, Card{
			Name:       name,
			DevicePath: device,
			HwmonPath:  firstHwmon(filepath.Join(device, "hwmon")),
			PCIAddr:    pciAddr,
		})
	}
	return cards, nil
}

// Identity is the per-card identity payload populated from sysfs.
// Values are best-effort; missing fields are returned as the empty string.
type Identity struct {
	Model         string // product_name or fallback
	VBIOSVersion  string // vbios_version
	Revision      string // revision
	KernelRelease string // /proc/sys/kernel/osrelease
}

// ReadIdentity gathers the identity fields for one card.
func (r *Reader) ReadIdentity(c Card) Identity {
	device := c.DevicePath
	model := firstNonEmpty(
		mustReadText(filepath.Join(device, "product_name")),
		mustReadText(filepath.Join(device, "subsystem_device")),
		mustReadText(filepath.Join(device, "device")),
	)
	if model == "" {
		model = "unknown"
	}
	return Identity{
		Model:         model,
		VBIOSVersion:  mustReadText(filepath.Join(device, "vbios_version")),
		Revision:      mustReadText(filepath.Join(device, "revision")),
		KernelRelease: mustReadText(filepath.Join(r.procRoot, "sys/kernel/osrelease")),
	}
}

// CardSnapshot is everything the sysfs backend reads from one card on
// one scrape, except per-process data which is keyed on PCI address
// across all cards (see ReadProcesses).
type CardSnapshot struct {
	Card       Card
	Identity   Identity
	GPUBusyPct *int64
	MemBusyPct *int64
	VRAMTotal  *int64
	VRAMUsed   *int64
	VisVRAMTot *int64
	VisVRAMUse *int64
	GTTTotal   *int64
	GTTUsed    *int64

	LinkSpeedGTs    *float64
	LinkSpeedMaxGTs *float64
	LinkWidth       *int64
	LinkWidthMax    *int64

	// Clocks lists every domain (sclk, mclk, …) the card exposes
	// through pp_dpm_*. Each entry has the active step and the
	// available range.
	Clocks []ClockReading

	// PerfLevel is the value of power_dpm_force_performance_level
	// (e.g. "auto", "low", "high", "manual").
	PerfLevel string

	// Hwmon collects power, fans, temps, voltages.
	Hwmon HwmonReadings
}

// ClockReading is one DPM-tracked clock domain on a card.
type ClockReading struct {
	Domain    string // "sclk", "mclk", "fclk", …
	ActiveMHz *int64
	MinMHz    int64
	MaxMHz    int64
}

// HwmonReadings is everything we pull out of one hwmon directory.
type HwmonReadings struct {
	Temps        []TempReading // per-sensor temperature in Celsius
	PowerW       *float64
	PowerCapW    *float64
	PowerCapDefW *float64
	PowerCapMaxW *float64
	Fans         []FanReading  // per-fan RPM + PWM
	Voltages     []VoltageRail // per-rail voltage in volts
}

// TempReading is one temperature sensor (edge / junction / mem / …).
type TempReading struct {
	Sensor  string
	Celsius float64
}

// FanReading is one fan tachometer + PWM duty pair.
type FanReading struct {
	Index  string
	RPM    *int64
	PWM    *int64
	PWMMax int64
	PWMPct *float64
}

// VoltageRail is one in*_input voltage reading.
type VoltageRail struct {
	Rail  string // "vddgfx", "vddnb"
	Volts float64
}

// ReadCard runs every sysfs read needed for one card.
func (r *Reader) ReadCard(c Card) CardSnapshot {
	// AMDGPU runtime power management suspends the GPU when no DRM
	// client has it open. While suspended, dynamic sysfs reads
	// (gpu_busy_percent, temp*_input, pp_dpm_*, hwmon/*) return EPERM
	// and a few "static" fields (current_link_width, current_link_speed)
	// return bogus values. Open a render node fd before reading and
	// hold it for the scrape — the AMD driver pm_runtime_get_sync's on
	// the open and stays active as long as the fd is live.
	closer := r.wakeGPU(c)
	defer closer()

	snap := CardSnapshot{
		Card:     c,
		Identity: r.ReadIdentity(c),
	}
	device := c.DevicePath

	snap.GPUBusyPct = readInt64(filepath.Join(device, "gpu_busy_percent"))
	snap.MemBusyPct = readInt64(filepath.Join(device, "mem_busy_percent"))

	for fname, target := range map[string]**int64{
		"mem_info_vram_total":     &snap.VRAMTotal,
		"mem_info_vram_used":      &snap.VRAMUsed,
		"mem_info_vis_vram_total": &snap.VisVRAMTot,
		"mem_info_vis_vram_used":  &snap.VisVRAMUse,
		"mem_info_gtt_total":      &snap.GTTTotal,
		"mem_info_gtt_used":       &snap.GTTUsed,
	} {
		*target = readInt64(filepath.Join(device, fname))
	}

	snap.LinkSpeedGTs = readGTPerSecond(filepath.Join(device, "current_link_speed"))
	snap.LinkSpeedMaxGTs = readGTPerSecond(filepath.Join(device, "max_link_speed"))
	snap.LinkWidth = readInt64(filepath.Join(device, "current_link_width"))
	snap.LinkWidthMax = readInt64(filepath.Join(device, "max_link_width"))

	snap.Clocks = readClocks(device)
	snap.PerfLevel = mustReadText(filepath.Join(device, "power_dpm_force_performance_level"))
	if c.HwmonPath != "" {
		snap.Hwmon = readHwmon(c.HwmonPath)
	}
	return snap
}

// ----- runtime PM wake -----------------------------------------------------

// wakeGPU opens the card's DRM render node (/dev/dri/renderD<minor>) so
// AMDGPU runtime PM resumes the GPU. Returns a no-op closer when the
// node can't be opened (best-effort — sysfs reads will then likely
// return EPERM, which the rest of the reader already tolerates).
//
// After opening the fd this polls runtime_status briefly until it shows
// "active" so the caller doesn't race the resume transition.
func (r *Reader) wakeGPU(c Card) (closer func()) {
	noop := func() {}
	renderPath := r.findRenderNode(c)
	if renderPath == "" {
		return noop
	}
	f, err := os.OpenFile(renderPath, os.O_RDONLY, 0)
	if err != nil {
		return noop
	}

	// Poll runtime_status — the resume from suspended takes ~50-300ms
	// on RDNA3. Bound at 500ms so a wedged card doesn't tank scrape
	// latency. If runtime_status doesn't exist (older kernel / non-AMD)
	// fall through after the first iteration.
	rsPath := filepath.Join(c.DevicePath, "power", "runtime_status")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		rs, err := readText(rsPath)
		if err != nil || rs == "active" || rs == "" {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return func() { _ = f.Close() }
}

// findRenderNode returns the /dev/dri/renderD<minor> path that
// corresponds to this card. It reads <device>/drm/renderD<N> if
// available (the kernel publishes the render minor as a sibling DRM
// minor under the device); falls back to /dev/dri/renderD128.
func (r *Reader) findRenderNode(c Card) string {
	drmDir := filepath.Join(c.DevicePath, "drm")
	if entries, err := os.ReadDir(drmDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, "renderD") {
				return filepath.Join(r.driRoot, name)
			}
		}
	}
	// Linux convention: render minor = card minor + 128. cardN -> renderD(128+N).
	if n := cardSuffix(c.Name); n >= 0 {
		return filepath.Join(r.driRoot, fmt.Sprintf("renderD%d", 128+n))
	}
	return ""
}

// cardSuffix returns the integer N from "cardN", or -1 on no match.
func cardSuffix(name string) int {
	if !strings.HasPrefix(name, "card") {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimPrefix(name, "card"))
	if err != nil {
		return -1
	}
	return n
}

// ----- DPM clock parsing ---------------------------------------------------

// pp_dpm_* files look like:
//
//	0: 500Mhz
//	1: 1500Mhz
//	2: 2304Mhz *
//
// The trailing '*' marks the active step.
var dpmRowRE = regexp.MustCompile(`^(\d+):\s*(\d+)\s*[A-Za-z]+(\s*\*)?$`)

// dpmDomains is the superset of pp_dpm_* files we probe. The set that
// actually exists varies by ASIC — missing ones are silently skipped.
var dpmDomains = []struct {
	domain string
	file   string
}{
	{"sclk", "pp_dpm_sclk"},
	{"mclk", "pp_dpm_mclk"},
	{"fclk", "pp_dpm_fclk"},
	{"socclk", "pp_dpm_socclk"},
	{"vclk", "pp_dpm_vclk"},
	{"dclk", "pp_dpm_dclk"},
	{"vclk0", "pp_dpm_vclk0"},
	{"dclk0", "pp_dpm_dclk0"},
	{"vclk1", "pp_dpm_vclk1"},
	{"dclk1", "pp_dpm_dclk1"},
	{"pcie", "pp_dpm_pcie"},
}

func readClocks(device string) []ClockReading {
	out := make([]ClockReading, 0, len(dpmDomains))
	for _, d := range dpmDomains {
		text, err := readText(filepath.Join(device, d.file))
		if err != nil || text == "" {
			continue
		}
		var (
			minVal, maxVal int64 = 1 << 62, 0
			active         *int64
			haveAny        bool
		)
		for _, line := range strings.Split(text, "\n") {
			m := dpmRowRE.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			val, err := strconv.ParseInt(m[2], 10, 64)
			if err != nil {
				continue
			}
			haveAny = true
			if val < minVal {
				minVal = val
			}
			if val > maxVal {
				maxVal = val
			}
			if m[3] != "" {
				v := val
				active = &v
			}
		}
		if !haveAny {
			continue
		}
		out = append(out, ClockReading{
			Domain:    d.domain,
			ActiveMHz: active,
			MinMHz:    minVal,
			MaxMHz:    maxVal,
		})
	}
	return out
}

// ----- hwmon -------------------------------------------------------------

func readHwmon(hwmon string) HwmonReadings {
	var h HwmonReadings

	for idx := 1; idx <= 5; idx++ {
		mc := readInt64(filepath.Join(hwmon, fmt.Sprintf("temp%d_input", idx)))
		if mc == nil {
			continue
		}
		label := mustReadText(filepath.Join(hwmon, fmt.Sprintf("temp%d_label", idx)))
		if label == "" {
			label = fmt.Sprintf("temp%d", idx)
		}
		h.Temps = append(h.Temps, TempReading{
			Sensor:  strings.ToLower(label),
			Celsius: float64(*mc) / 1000.0,
		})
	}

	if uw := readInt64(filepath.Join(hwmon, "power1_average")); uw != nil {
		v := float64(*uw) / 1_000_000.0
		h.PowerW = &v
	}
	for fname, dst := range map[string]**float64{
		"power1_cap":         &h.PowerCapW,
		"power1_cap_default": &h.PowerCapDefW,
		"power1_cap_max":     &h.PowerCapMaxW,
	} {
		if uw := readInt64(filepath.Join(hwmon, fname)); uw != nil {
			v := float64(*uw) / 1_000_000.0
			*dst = &v
		}
	}

	for idx := 1; idx <= 5; idx++ {
		fan := FanReading{Index: strconv.Itoa(idx)}
		fan.RPM = readInt64(filepath.Join(hwmon, fmt.Sprintf("fan%d_input", idx)))
		fan.PWM = readInt64(filepath.Join(hwmon, fmt.Sprintf("pwm%d", idx)))
		fan.PWMMax = 255
		if pwmMax := readInt64(filepath.Join(hwmon, fmt.Sprintf("pwm%d_max", idx))); pwmMax != nil && *pwmMax > 0 {
			fan.PWMMax = *pwmMax
		}
		if fan.PWM != nil {
			pct := 100.0 * float64(*fan.PWM) / float64(fan.PWMMax)
			fan.PWMPct = &pct
		}
		if fan.RPM == nil && fan.PWM == nil {
			continue
		}
		h.Fans = append(h.Fans, fan)
	}

	rails := []struct {
		file string
		rail string
	}{
		{"in0_input", "vddgfx"},
		{"in1_input", "vddnb"},
	}
	for _, r := range rails {
		if mv := readInt64(filepath.Join(hwmon, r.file)); mv != nil {
			h.Voltages = append(h.Voltages, VoltageRail{
				Rail:  r.rail,
				Volts: float64(*mv) / 1000.0,
			})
		}
	}
	return h
}

// ----- helpers -----------------------------------------------------------

var cardNameRE = regexp.MustCompile(`^card\d+$`)

func firstHwmon(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "hwmon") {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // sysfs paths are bounded by Discover
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func mustReadText(path string) string {
	s, err := readText(path)
	if err != nil {
		return ""
	}
	return s
}

func readInt64(path string) *int64 {
	s, err := readText(path)
	if err != nil || s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return nil
	}
	return &v
}

// readGTPerSecond parses sysfs values like "16.0 GT/s PCIe" or
// "8.0 GT/s" and returns the leading float.
func readGTPerSecond(path string) *float64 {
	s, err := readText(path)
	if err != nil || s == "" {
		return nil
	}
	for _, tok := range strings.Fields(s) {
		v, err := strconv.ParseFloat(tok, 64)
		if err == nil {
			return &v
		}
	}
	return nil
}

func firstNonEmpty(in ...string) string {
	for _, s := range in {
		if s != "" {
			return s
		}
	}
	return ""
}
