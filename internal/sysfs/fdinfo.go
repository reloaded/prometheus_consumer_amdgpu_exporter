package sysfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// /proc/<pid>/fdinfo/<fd> for amdgpu DRM fds includes lines like:
//
//	drm-driver:      amdgpu
//	drm-pdev:        0000:03:00.0
//	drm-client-id:   42
//	drm-engine-gfx:  1234567 ns
//	drm-engine-compute: 0 ns
//	drm-memory-vram: 524288 KiB
//	drm-memory-gtt:  131072 KiB
//
// Per-engine totals are monotonic counters; per-memory values are
// the per-client current allocation. The same client typically holds
// many fds reporting identical totals — we take the max across fds
// of one PID rather than summing.
var (
	fdEngineRE = regexp.MustCompile(`^drm-engine-([a-z0-9_]+):\s+(\d+)\s+ns$`)
	fdMemRE    = regexp.MustCompile(`^(drm-memory-[a-z\-]+):\s+(\d+)\s+(KiB|MiB|GiB)$`)
)

// ProcessUsage is per-PID amdgpu accounting for one card.
type ProcessUsage struct {
	PID  int
	Comm string
	// PCIAddr is the card BDF as printed by the driver (matches
	// sysfs's resolved device target, e.g. "0000:03:00.0").
	PCIAddr string
	// MemoryBytes is keyed by the raw fdinfo key
	// (drm-memory-vram, drm-memory-gtt, drm-memory-vis-vram, …).
	MemoryBytes map[string]int64
	// EngineNS is keyed by engine name (gfx, compute, enc, dec, …).
	EngineNS map[string]int64
}

// ReadProcesses walks /proc and returns per-PID amdgpu accounting,
// keyed by PCI address. Returns nil if /proc is unreadable.
//
// Reading foreign PIDs' fdinfo requires CAP_SYS_PTRACE or running as
// root with hostPID=true — see the deploying Ansible role.
func (r *Reader) ReadProcesses() (map[string][]ProcessUsage, error) {
	entries, err := os.ReadDir(r.procRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string][]ProcessUsage)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		fdinfoDir := filepath.Join(r.procRoot, e.Name(), "fdinfo")
		fds, err := os.ReadDir(fdinfoDir)
		if err != nil {
			continue
		}
		perPDevMem := make(map[string]map[string]int64)
		perPDevEng := make(map[string]map[string]int64)
		for _, fd := range fds {
			path := filepath.Join(fdinfoDir, fd.Name())
			pdev, mem, eng, ok := parseFdinfo(path)
			if !ok {
				continue
			}
			memAcc := perPDevMem[pdev]
			if memAcc == nil {
				memAcc = make(map[string]int64)
				perPDevMem[pdev] = memAcc
			}
			for k, v := range mem {
				if v > memAcc[k] {
					memAcc[k] = v
				}
			}
			engAcc := perPDevEng[pdev]
			if engAcc == nil {
				engAcc = make(map[string]int64)
				perPDevEng[pdev] = engAcc
			}
			for k, v := range eng {
				if v > engAcc[k] {
					engAcc[k] = v
				}
			}
		}
		if len(perPDevMem) == 0 && len(perPDevEng) == 0 {
			continue
		}
		comm := mustReadText(filepath.Join(r.procRoot, e.Name(), "comm"))
		if len(comm) > 64 {
			comm = comm[:64]
		}
		seen := make(map[string]struct{})
		for pdev := range perPDevMem {
			seen[pdev] = struct{}{}
		}
		for pdev := range perPDevEng {
			seen[pdev] = struct{}{}
		}
		for pdev := range seen {
			out[pdev] = append(out[pdev], ProcessUsage{
				PID:         pid,
				Comm:        comm,
				PCIAddr:     pdev,
				MemoryBytes: perPDevMem[pdev],
				EngineNS:    perPDevEng[pdev],
			})
		}
	}
	return out, nil
}

func parseFdinfo(path string) (pdev string, mem map[string]int64, eng map[string]int64, ok bool) {
	b, err := os.ReadFile(path) //nolint:gosec // path bounded by /proc walk
	if err != nil {
		return "", nil, nil, false
	}
	text := string(b)
	if !strings.Contains(text, "drm-driver") {
		return "", nil, nil, false
	}
	mem = make(map[string]int64)
	eng = make(map[string]int64)
	isAmdgpu := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "drm-driver:"):
			isAmdgpu = strings.TrimSpace(strings.TrimPrefix(line, "drm-driver:")) == "amdgpu"
		case strings.HasPrefix(line, "drm-pdev:"):
			pdev = strings.TrimSpace(strings.TrimPrefix(line, "drm-pdev:"))
		default:
			if m := fdEngineRE.FindStringSubmatch(line); m != nil {
				if v, err := strconv.ParseInt(m[2], 10, 64); err == nil {
					eng[m[1]] = v
				}
				continue
			}
			if m := fdMemRE.FindStringSubmatch(line); m != nil {
				v, err := strconv.ParseInt(m[2], 10, 64)
				if err != nil {
					continue
				}
				switch m[3] {
				case "KiB":
					v *= 1024
				case "MiB":
					v *= 1024 * 1024
				case "GiB":
					v *= 1024 * 1024 * 1024
				}
				mem[m[1]] = v
			}
		}
	}
	if !isAmdgpu || pdev == "" {
		return "", nil, nil, false
	}
	return pdev, mem, eng, true
}
