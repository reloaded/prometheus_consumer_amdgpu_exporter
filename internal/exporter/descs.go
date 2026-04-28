package exporter

import "github.com/prometheus/client_golang/prometheus"

const namespace = "amdgpu"

// collectorDescs holds every prometheus.Desc the exporter emits. Built
// once at startup and reused across scrapes.
type collectorDescs struct {
	nodesTotal *prometheus.Desc
	info       *prometheus.Desc

	gpuBusy *prometheus.Desc
	memBusy *prometheus.Desc

	temperature *prometheus.Desc
	power       *prometheus.Desc
	powerCap    *prometheus.Desc
	powerCapDef *prometheus.Desc
	powerCapMax *prometheus.Desc

	fanRPM    *prometheus.Desc
	fanPWM    *prometheus.Desc
	fanPWMPct *prometheus.Desc

	voltage *prometheus.Desc

	clock    *prometheus.Desc
	clockMin *prometheus.Desc
	clockMax *prometheus.Desc

	vramTotal  *prometheus.Desc
	vramUsed   *prometheus.Desc
	visVRAMTot *prometheus.Desc
	visVRAMUse *prometheus.Desc
	gttTotal   *prometheus.Desc
	gttUsed    *prometheus.Desc

	linkSpeed    *prometheus.Desc
	linkSpeedMax *prometheus.Desc
	linkWidth    *prometheus.Desc
	linkWidthMax *prometheus.Desc

	perfLevel *prometheus.Desc

	procVRAM   *prometheus.Desc
	procGTT    *prometheus.Desc
	procEngine *prometheus.Desc
}

func newCollectorDescs() collectorDescs {
	gpuInst := []string{"gpu", "instance"}
	gpuFanInst := []string{"gpu", "fan", "instance"}
	gpuRailInst := []string{"gpu", "rail", "instance"}
	gpuSensorInst := []string{"gpu", "sensor", "instance"}
	gpuDomainInst := []string{"gpu", "domain", "instance"}
	gpuPidCommInst := []string{"gpu", "pid", "comm", "instance"}
	gpuEnginePidCommInst := []string{"gpu", "engine", "pid", "comm", "instance"}

	d := func(name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, labels, nil)
	}

	return collectorDescs{
		nodesTotal: d(namespace+"_nodes_total", "Number of AMD GPUs on this node.", []string{"instance"}),

		info: d(namespace+"_info",
			"AMD GPU identity (always 1). Labels carry sysfs identity plus optional amd-smi-sourced uuid / vbios_date / market_name.",
			[]string{"gpu", "instance", "card", "pci_addr", "model", "vbios", "revision", "kernel", "uuid", "market_name", "vbios_date"}),

		gpuBusy: d(namespace+"_gpu_busy_percent", "Graphics engine busy percentage (0-100).", gpuInst),
		memBusy: d(namespace+"_memory_busy_percent", "Memory controller busy percentage (0-100).", gpuInst),

		temperature: d(namespace+"_temperature_celsius", "Sensor temperature in degrees Celsius.", gpuSensorInst),

		power:       d(namespace+"_power_usage_watts", "Average package power draw in Watts.", gpuInst),
		powerCap:    d(namespace+"_power_cap_watts", "Currently configured power cap in Watts.", gpuInst),
		powerCapDef: d(namespace+"_power_cap_default_watts", "Default power cap (board TDP) in Watts.", gpuInst),
		powerCapMax: d(namespace+"_power_cap_max_watts", "Maximum allowed power cap in Watts.", gpuInst),

		fanRPM:    d(namespace+"_fan_speed_rpm", "Fan tachometer reading in RPM.", gpuFanInst),
		fanPWM:    d(namespace+"_fan_pwm", "Fan PWM duty (0-255).", gpuFanInst),
		fanPWMPct: d(namespace+"_fan_pwm_percent", "Fan PWM duty (0-100).", gpuFanInst),

		voltage: d(namespace+"_voltage_volts", "Voltage rail reading in Volts.", gpuRailInst),

		clock:    d(namespace+"_clock_mhz", "Active clock frequency in MHz (from pp_dpm_*).", gpuDomainInst),
		clockMin: d(namespace+"_clock_min_mhz", "Lowest available DPM frequency in MHz.", gpuDomainInst),
		clockMax: d(namespace+"_clock_max_mhz", "Highest available DPM frequency in MHz.", gpuDomainInst),

		vramTotal:  d(namespace+"_vram_total_bytes", "Total VRAM in bytes.", gpuInst),
		vramUsed:   d(namespace+"_vram_used_bytes", "Used VRAM in bytes.", gpuInst),
		visVRAMTot: d(namespace+"_vram_visible_total_bytes", "Total CPU-visible VRAM (BAR window) in bytes.", gpuInst),
		visVRAMUse: d(namespace+"_vram_visible_used_bytes", "Used CPU-visible VRAM in bytes.", gpuInst),
		gttTotal:   d(namespace+"_gtt_total_bytes", "Total GTT (graphics translation table) memory in bytes.", gpuInst),
		gttUsed:    d(namespace+"_gtt_used_bytes", "Used GTT memory in bytes.", gpuInst),

		linkSpeed:    d(namespace+"_pcie_link_speed_gts", "Current PCIe link speed in GT/s.", gpuInst),
		linkSpeedMax: d(namespace+"_pcie_link_speed_max_gts", "Maximum PCIe link speed the card negotiated to in GT/s.", gpuInst),
		linkWidth:    d(namespace+"_pcie_link_width", "Current PCIe link width (lanes).", gpuInst),
		linkWidthMax: d(namespace+"_pcie_link_width_max", "Maximum PCIe link width (lanes).", gpuInst),

		perfLevel: d(namespace+"_performance_level_info",
			"Active power_dpm_force_performance_level value as a {level=...}=1 gauge.",
			[]string{"gpu", "level", "instance"}),

		procVRAM:   d(namespace+"_process_vram_bytes", "Per-process VRAM usage in bytes (drm-memory-vram from fdinfo).", gpuPidCommInst),
		procGTT:    d(namespace+"_process_gtt_bytes", "Per-process GTT usage in bytes (drm-memory-gtt from fdinfo).", gpuPidCommInst),
		procEngine: d(namespace+"_process_engine_busy_seconds_total", "Per-process per-engine cumulative busy time (drm-engine-* from fdinfo).", gpuEnginePidCommInst),
	}
}

func (d collectorDescs) describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		d.nodesTotal, d.info,
		d.gpuBusy, d.memBusy,
		d.temperature,
		d.power, d.powerCap, d.powerCapDef, d.powerCapMax,
		d.fanRPM, d.fanPWM, d.fanPWMPct,
		d.voltage,
		d.clock, d.clockMin, d.clockMax,
		d.vramTotal, d.vramUsed, d.visVRAMTot, d.visVRAMUse, d.gttTotal, d.gttUsed,
		d.linkSpeed, d.linkSpeedMax, d.linkWidth, d.linkWidthMax,
		d.perfLevel,
		d.procVRAM, d.procGTT, d.procEngine,
	} {
		ch <- desc
	}
}
