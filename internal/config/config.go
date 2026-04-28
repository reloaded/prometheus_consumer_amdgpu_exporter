// Package config holds the runtime configuration for the exporter.
//
// All settings come from environment variables and CLI flags — there is
// no config file. The exporter is meant to be deployed by an Ansible role
// or similar that already owns its own config surface; carrying a second
// YAML layer here would be redundant.
package config

import "time"

// Config is the resolved configuration after env/flag parsing.
type Config struct {
	// Hostname is the value used for the `instance` label on every
	// emitted series. Defaults to $NODE_NAME or os.Hostname().
	Hostname string

	// EnableSysfs toggles the sysfs/hwmon/fdinfo backend. On by default.
	EnableSysfs bool

	// EnableAmdSMI toggles the amd-smi shell-out backend. Off by default.
	// When on but the binary is missing, the backend logs a warning and
	// contributes no metrics — the exporter stays runnable.
	EnableAmdSMI bool

	// CollectProc enables the /proc/<pid>/fdinfo walk that produces
	// per-process VRAM / engine series. Implied false when EnableSysfs
	// is false.
	CollectProc bool

	// AmdSMIPath is the absolute or PATH-relative location of the
	// `amd-smi` binary. Resolved at scrape time, not at startup, so
	// missing-binary failures don't crash the exporter.
	AmdSMIPath string

	// AmdSMITimeout caps a single amd-smi invocation. amd-smi can hang
	// briefly on first run while it initialises ROCm; default 5s.
	AmdSMITimeout time.Duration

	// SysfsRoot is the path to the /sys/class/drm root. Overridable for
	// tests; defaults to /sys/class/drm.
	SysfsRoot string

	// ProcRoot is the path to /proc. Overridable for tests; defaults to
	// /proc.
	ProcRoot string
}
