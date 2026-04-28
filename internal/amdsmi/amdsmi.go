// Package amdsmi is the optional shell-out backend that runs
// `amd-smi static --json` and merges the result into the exporter's
// per-card identity labels.
//
// Best-effort: failures (missing binary, timeout, parse errors) are
// logged at warn level and the backend silently contributes nothing
// to that scrape. The sysfs backend keeps working regardless. This
// keeps the same image deployable on hosts with and without ROCm.
package amdsmi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Backend wraps the amd-smi binary.
type Backend struct {
	binary  string
	timeout time.Duration

	// missingLogged is set after the first "binary not found" warning
	// so the log doesn't fill up if amd-smi never appears.
	missingLogged bool
}

// New returns a Backend bound to the given binary path. binary may be
// an absolute path or a name to look up via $PATH.
func New(binary string, timeout time.Duration) *Backend {
	if binary == "" {
		binary = "amd-smi"
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Backend{binary: binary, timeout: timeout}
}

// Static is the per-card identity payload sourced from amd-smi.
// All fields are optional. The keys match what we add to the
// `amdgpu_info` metric's label set.
type Static struct {
	UUID            string
	MarketName      string
	ASICSerial      string
	DeviceID        string
	VendorID        string
	VBIOSVersion    string
	VBIOSDate       string
	VBIOSPartNumber string
}

// FetchStatic runs `amd-smi static --json` and returns a map from
// PCI BDF (e.g. "0000:03:00.0") to identity fields. Returns an empty
// map on any failure.
func (b *Backend) FetchStatic(ctx context.Context, logger *slog.Logger) map[string]Static {
	if _, err := exec.LookPath(b.binary); err != nil {
		if !b.missingLogged {
			logger.Warn("amd-smi binary not found; backend disabled for this scrape",
				"binary", b.binary)
			b.missingLogged = true
		}
		return map[string]Static{}
	}
	// Reset the warning latch so a binary that goes missing later in
	// the run logs again.
	b.missingLogged = false

	cctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, b.binary, "static", "--json") //nolint:gosec // binary path is operator-controlled
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			logger.Warn("amd-smi static --json exited non-zero",
				"code", ee.ExitCode(),
				"stderr", trim(string(ee.Stderr)),
			)
		} else {
			logger.Warn("amd-smi static --json failed", "err", err)
		}
		return map[string]Static{}
	}
	parsed, err := parseStatic(out)
	if err != nil {
		logger.Warn("amd-smi static --json: parse error", "err", err)
		return map[string]Static{}
	}
	return parsed
}

// parseStatic accepts both the array shape (`[ {...}, {...} ]`) and
// the older single-object shape produced by some amd-smi releases,
// then rummages for fields under the keys we know about. Anything
// we can't recognise is silently dropped.
func parseStatic(raw []byte) (map[string]Static, error) {
	var anyDoc any
	if err := json.Unmarshal(raw, &anyDoc); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	rows := flattenRows(anyDoc)
	out := make(map[string]Static, len(rows))
	for _, row := range rows {
		bdf := pickString(row,
			[]string{"bus", "bdf"},
			[]string{"bdf"},
			[]string{"pci", "bdf"},
		)
		if bdf == "" {
			continue
		}
		s := Static{
			UUID:            pickString(row, []string{"uuid"}, []string{"kfd", "uuid"}),
			MarketName:      pickString(row, []string{"asic", "market_name"}, []string{"market_name"}),
			ASICSerial:      pickString(row, []string{"asic", "asic_serial"}, []string{"asic_serial"}),
			DeviceID:        pickString(row, []string{"asic", "device_id"}, []string{"device_id"}),
			VendorID:        pickString(row, []string{"asic", "vendor_id"}, []string{"vendor_id"}),
			VBIOSVersion:    pickString(row, []string{"vbios", "version"}, []string{"vbios_version"}),
			VBIOSDate:       pickString(row, []string{"vbios", "date"}, []string{"vbios_date"}),
			VBIOSPartNumber: pickString(row, []string{"vbios", "part_number"}, []string{"vbios_part_number"}),
		}
		out[strings.ToLower(bdf)] = s
	}
	return out, nil
}

// flattenRows accepts either a []map or a single map and returns it
// as a slice of maps. Returns nil for anything else.
func flattenRows(doc any) []map[string]any {
	switch v := doc.(type) {
	case []any:
		var out []map[string]any
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{v}
	default:
		return nil
	}
}

// pickString walks the row through any of the given key paths and
// returns the first non-empty string it finds. Numeric values are
// stringified; "N/A" is treated as empty.
func pickString(row map[string]any, paths ...[]string) string {
	for _, path := range paths {
		var cur any = row
		ok := true
		for _, key := range path {
			m, isMap := cur.(map[string]any)
			if !isMap {
				ok = false
				break
			}
			cur = m[key]
		}
		if !ok || cur == nil {
			continue
		}
		switch v := cur.(type) {
		case string:
			s := strings.TrimSpace(v)
			if s == "" || strings.EqualFold(s, "n/a") {
				continue
			}
			return s
		case float64:
			return fmt.Sprintf("%v", v)
		case bool:
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
