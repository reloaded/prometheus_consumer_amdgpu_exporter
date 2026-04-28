package sysfs

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFdinfo(t *testing.T, proc, pid string, fds map[string]string) {
	t.Helper()
	dir := filepath.Join(proc, pid, "fdinfo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, pid, "comm"), []byte("ollama\n"), 0o644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	for name, content := range fds {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	}
}

const goodFdinfo = `pos:    0
flags:    02
mnt_id:    24
ino:    1234
drm-driver:    amdgpu
drm-pdev:    0000:03:00.0
drm-client-id:    42
drm-engine-gfx:    1234567 ns
drm-engine-compute:    0 ns
drm-memory-vram:    524288 KiB
drm-memory-gtt:    131072 KiB
drm-memory-cpu:    0 KiB
`

const otherDriverFdinfo = `pos:    0
drm-driver:    nvidia
drm-pdev:    0000:01:00.0
drm-memory-vram:    1024 KiB
`

func TestReadProcessesAggregatesAcrossFds(t *testing.T) {
	proc := t.TempDir()
	writeFdinfo(t, proc, "1234", map[string]string{
		"3":   goodFdinfo,
		"4":   goodFdinfo, // duplicate (same client) — must not double-count
		"non": "junk file with no drm-driver",
	})
	writeFdinfo(t, proc, "5678", map[string]string{"3": otherDriverFdinfo})

	r := New(t.TempDir(), proc)
	got, err := r.ReadProcesses()
	if err != nil {
		t.Fatal(err)
	}
	procs, ok := got["0000:03:00.0"]
	if !ok {
		t.Fatalf("missing pdev, got keys: %v", keys(got))
	}
	if len(procs) != 1 {
		t.Fatalf("got %d procs for pdev, want 1", len(procs))
	}
	p := procs[0]
	if p.PID != 1234 {
		t.Errorf("pid = %d", p.PID)
	}
	if p.Comm != "ollama" {
		t.Errorf("comm = %q", p.Comm)
	}
	if p.MemoryBytes["drm-memory-vram"] != 524288*1024 {
		t.Errorf("vram = %d", p.MemoryBytes["drm-memory-vram"])
	}
	if p.MemoryBytes["drm-memory-gtt"] != 131072*1024 {
		t.Errorf("gtt = %d", p.MemoryBytes["drm-memory-gtt"])
	}
	if p.EngineNS["gfx"] != 1234567 {
		t.Errorf("gfx ns = %d", p.EngineNS["gfx"])
	}
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
