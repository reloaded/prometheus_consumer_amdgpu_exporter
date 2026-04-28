// Package exporter wires the sysfs and amd-smi backends behind a single
// prometheus.Collector. One scrape == one Discover() call (for sysfs) +
// one ReadProcesses() call (if enabled) + one amd-smi shell-out (if
// enabled). Each backend can be toggled independently via Config.
package exporter

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/amdsmi"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/config"
	"github.com/reloaded/prometheus_consumer_amdgpu_exporter/internal/sysfs"
)

// Collector is the prometheus.Collector that emits every series the
// exporter knows about.
type Collector struct {
	cfg    config.Config
	sys    *sysfs.Reader
	smi    *amdsmi.Backend
	logger *slog.Logger

	// Pre-built descriptors. Keeping them as fields (rather than re-
	// allocating in Collect) makes Describe trivial and saves churn on
	// scrape-per-second exporters.
	descs collectorDescs
}

// NewCollector builds a Collector with the given backends.
func NewCollector(cfg config.Config, sys *sysfs.Reader, smi *amdsmi.Backend, logger *slog.Logger) *Collector {
	return &Collector{
		cfg:    cfg,
		sys:    sys,
		smi:    smi,
		logger: logger,
		descs:  newCollectorDescs(),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.descs.describe(ch)
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	host := c.cfg.Hostname

	cards, err := c.sys.Discover()
	if err != nil {
		c.logger.Error("sysfs discover failed", "err", err)
	}
	if !c.cfg.EnableSysfs {
		// Force-empty so the per-card loop is a no-op below.
		cards = nil
	}

	ch <- prometheus.MustNewConstMetric(
		c.descs.nodesTotal, prometheus.GaugeValue, float64(len(cards)), host,
	)

	staticByPDev := map[string]amdsmi.Static{}
	if c.cfg.EnableAmdSMI {
		staticByPDev = c.smi.FetchStatic(context.Background(), c.logger)
	}

	for _, card := range cards {
		snap := c.sys.ReadCard(card)
		c.emitCard(ch, host, snap, staticByPDev[normaliseBDF(card.PCIAddr)])
	}

	if c.cfg.EnableSysfs && c.cfg.CollectProc && len(cards) > 0 {
		c.emitProcesses(ch, host, cards)
	}
}

func (c *Collector) emitCard(ch chan<- prometheus.Metric, host string, snap sysfs.CardSnapshot, smiStatic amdsmi.Static) {
	gpu := snap.Card.Name
	id := snap.Identity

	// amdgpu_info — info-style gauge that always emits 1, with one
	// label per identity field. amd-smi values fill in keys sysfs
	// doesn't supply (uuid, vbios_date, market_name); they don't
	// shadow sysfs values where both are populated.
	uuid := smiStatic.UUID
	market := smiStatic.MarketName
	vbiosDate := smiStatic.VBIOSDate
	ch <- prometheus.MustNewConstMetric(
		c.descs.info, prometheus.GaugeValue, 1.0,
		gpu, host,
		snap.Card.Name, snap.Card.PCIAddr,
		id.Model, id.VBIOSVersion, id.Revision, id.KernelRelease,
		uuid, market, vbiosDate,
	)

	if v := snap.GPUBusyPct; v != nil {
		ch <- prometheus.MustNewConstMetric(c.descs.gpuBusy, prometheus.GaugeValue, float64(*v), gpu, host)
	}
	if v := snap.MemBusyPct; v != nil {
		ch <- prometheus.MustNewConstMetric(c.descs.memBusy, prometheus.GaugeValue, float64(*v), gpu, host)
	}

	emitInt64 := func(d *prometheus.Desc, v *int64) {
		if v == nil {
			return
		}
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, float64(*v), gpu, host)
	}
	emitInt64(c.descs.vramTotal, snap.VRAMTotal)
	emitInt64(c.descs.vramUsed, snap.VRAMUsed)
	emitInt64(c.descs.visVRAMTot, snap.VisVRAMTot)
	emitInt64(c.descs.visVRAMUse, snap.VisVRAMUse)
	emitInt64(c.descs.gttTotal, snap.GTTTotal)
	emitInt64(c.descs.gttUsed, snap.GTTUsed)

	if v := snap.LinkSpeedGTs; v != nil {
		ch <- prometheus.MustNewConstMetric(c.descs.linkSpeed, prometheus.GaugeValue, *v, gpu, host)
	}
	if v := snap.LinkSpeedMaxGTs; v != nil {
		ch <- prometheus.MustNewConstMetric(c.descs.linkSpeedMax, prometheus.GaugeValue, *v, gpu, host)
	}
	emitInt64(c.descs.linkWidth, snap.LinkWidth)
	emitInt64(c.descs.linkWidthMax, snap.LinkWidthMax)

	for _, clk := range snap.Clocks {
		ch <- prometheus.MustNewConstMetric(c.descs.clockMin, prometheus.GaugeValue, float64(clk.MinMHz), gpu, clk.Domain, host)
		ch <- prometheus.MustNewConstMetric(c.descs.clockMax, prometheus.GaugeValue, float64(clk.MaxMHz), gpu, clk.Domain, host)
		if clk.ActiveMHz != nil {
			ch <- prometheus.MustNewConstMetric(c.descs.clock, prometheus.GaugeValue, float64(*clk.ActiveMHz), gpu, clk.Domain, host)
		}
	}

	if snap.PerfLevel != "" {
		ch <- prometheus.MustNewConstMetric(c.descs.perfLevel, prometheus.GaugeValue, 1.0, gpu, snap.PerfLevel, host)
	}

	for _, t := range snap.Hwmon.Temps {
		ch <- prometheus.MustNewConstMetric(c.descs.temperature, prometheus.GaugeValue, t.Celsius, gpu, t.Sensor, host)
	}
	emitFloat := func(d *prometheus.Desc, v *float64) {
		if v == nil {
			return
		}
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, *v, gpu, host)
	}
	emitFloat(c.descs.power, snap.Hwmon.PowerW)
	emitFloat(c.descs.powerCap, snap.Hwmon.PowerCapW)
	emitFloat(c.descs.powerCapDef, snap.Hwmon.PowerCapDefW)
	emitFloat(c.descs.powerCapMax, snap.Hwmon.PowerCapMaxW)

	for _, fan := range snap.Hwmon.Fans {
		if fan.RPM != nil {
			ch <- prometheus.MustNewConstMetric(c.descs.fanRPM, prometheus.GaugeValue, float64(*fan.RPM), gpu, fan.Index, host)
		}
		if fan.PWM != nil {
			ch <- prometheus.MustNewConstMetric(c.descs.fanPWM, prometheus.GaugeValue, float64(*fan.PWM), gpu, fan.Index, host)
		}
		if fan.PWMPct != nil {
			ch <- prometheus.MustNewConstMetric(c.descs.fanPWMPct, prometheus.GaugeValue, *fan.PWMPct, gpu, fan.Index, host)
		}
	}

	for _, v := range snap.Hwmon.Voltages {
		ch <- prometheus.MustNewConstMetric(c.descs.voltage, prometheus.GaugeValue, v.Volts, gpu, v.Rail, host)
	}
}

func (c *Collector) emitProcesses(ch chan<- prometheus.Metric, host string, cards []sysfs.Card) {
	procs, err := c.sys.ReadProcesses()
	if err != nil {
		c.logger.Warn("fdinfo walk failed", "err", err)
		return
	}
	gpuByPDev := make(map[string]string, len(cards))
	for _, card := range cards {
		gpuByPDev[normaliseBDF(card.PCIAddr)] = card.Name
	}
	for pdev, list := range procs {
		gpu, ok := gpuByPDev[normaliseBDF(pdev)]
		if !ok {
			continue
		}
		for _, p := range list {
			pidStr := strconv.Itoa(p.PID)
			if v, ok := p.MemoryBytes["drm-memory-vram"]; ok {
				ch <- prometheus.MustNewConstMetric(c.descs.procVRAM, prometheus.GaugeValue, float64(v), gpu, pidStr, p.Comm, host)
			}
			if v, ok := p.MemoryBytes["drm-memory-gtt"]; ok {
				ch <- prometheus.MustNewConstMetric(c.descs.procGTT, prometheus.GaugeValue, float64(v), gpu, pidStr, p.Comm, host)
			}
			for engine, ns := range p.EngineNS {
				ch <- prometheus.MustNewConstMetric(
					c.descs.procEngine, prometheus.CounterValue,
					float64(ns)/1_000_000_000.0,
					gpu, engine, pidStr, p.Comm, host,
				)
			}
		}
	}
}

func normaliseBDF(s string) string {
	// sysfs sometimes returns the BDF in lowercase, sometimes mixed
	// case; amd-smi tends to lowercase. Folding to lower keeps the
	// join stable.
	return toLower(s)
}

func toLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
