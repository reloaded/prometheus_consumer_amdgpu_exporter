// Command prometheus_consumer_amdgpu_exporter is a Prometheus exporter for
// consumer-grade AMD GPUs (RDNA / RDNA2 / RDNA3).
//
// Two collection backends, mix-and-match:
//
//   - sysfs (default-on): reads /sys/class/drm, the matching hwmon, and
//     /proc/<pid>/fdinfo. Zero runtime dependencies.
//   - amd-smi (default-off): shells out to the ROCm `amd-smi` binary for
//     the handful of identity fields sysfs doesn't expose (KFD UUID,
//     VBIOS date, market name). Skipped silently if the binary is
//     missing.
//
// Both backends are toggled via env vars / flags so the same image
// works on hosts with and without ROCm installed. See README for the
// full configuration reference.
//
// Designed to live next to (not replace) ROCm's own
// device-metrics-exporter — that one is built for AMD Instinct
// accelerators (MI200/MI300) and leaves most of its metrics empty
// on consumer cards (most ECC / XGMI / HBM / partition counters
// don't apply to a Radeon RX, and a chunk of the rest read back
// as 0). This exporter targets the consumer side of the line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/amdsmi"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/config"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/exporter"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/sysfs"
)

// Version metadata is injected at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const envPrefix = "CONSUMER_AMDGPU_EXPORTER_"

func main() {
	var (
		listenAddr = flag.String("web.listen-address",
			envOr(envPrefix+"LISTEN_ADDR", ":9504"),
			"Address to listen on for /metrics.")
		metricsPath = flag.String("web.telemetry-path",
			envOr(envPrefix+"METRICS_PATH", "/metrics"),
			"Path under which collector metrics are exposed.")
		logLevel = flag.String("log-level",
			envOr(envPrefix+"LOG_LEVEL", "info"),
			"Log level: debug, info, warn, error.")
		amdSMIPath = flag.String("amd-smi.path",
			envOr(envPrefix+"AMD_SMI_PATH", "amd-smi"),
			"Path to the amd-smi binary. Resolved through $PATH if unqualified.")
		showVersion = flag.Bool("version", false, "Print version information and exit.")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("prometheus_consumer_amdgpu_exporter version=%s commit=%s built=%s\n", version, commit, date)
		return
	}

	logger := newLogger(*logLevel)
	slog.SetDefault(logger)

	cfg := config.Config{
		Hostname:      hostname(),
		EnableSysfs:   envBool(envPrefix+"BACKEND_SYSFS", true),
		EnableAmdSMI:  envBool(envPrefix+"BACKEND_AMD_SMI", false),
		CollectProc:   envBool(envPrefix+"COLLECT_PROCESSES", true),
		AmdSMIPath:    *amdSMIPath,
		AmdSMITimeout: envDuration(envPrefix+"AMD_SMI_TIMEOUT", 5*time.Second),
		SysfsRoot:     envOr(envPrefix+"SYSFS_ROOT", sysfs.DefaultRoot),
		ProcRoot:      envOr(envPrefix+"PROC_ROOT", sysfs.DefaultProc),
	}
	if !cfg.EnableSysfs && !cfg.EnableAmdSMI {
		logger.Warn("both backends disabled — exporter will emit no GPU metrics")
	}

	sysfsReader := sysfs.New(cfg.SysfsRoot, cfg.ProcRoot)
	defer func() { _ = sysfsReader.Close() }()

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
		exporter.NewCollector(cfg,
			sysfsReader,
			amdsmi.New(cfg.AmdSMIPath, cfg.AmdSMITimeout),
			logger,
		),
	)

	mux := http.NewServeMux()
	mux.Handle(*metricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<!doctype html>
<html><head><title>prometheus_consumer_amdgpu_exporter</title></head><body>
<h1>prometheus_consumer_amdgpu_exporter</h1>
<p>version: %s</p>
<ul>
  <li><a href="%s">%s</a></li>
  <li><a href="/healthz">/healthz</a></li>
</ul>
</body></html>
`, version, *metricsPath, *metricsPath)
	})

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server listening",
			"addr", *listenAddr,
			"hostname", cfg.Hostname,
			"sysfs", cfg.EnableSysfs,
			"amd_smi", cfg.EnableAmdSMI,
			"collect_processes", cfg.CollectProc,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y", "t":
		return true
	case "0", "false", "no", "off", "n", "f", "":
		return false
	default:
		return fallback
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func hostname() string {
	if v, ok := os.LookupEnv("NODE_NAME"); ok && v != "" {
		return v
	}
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
