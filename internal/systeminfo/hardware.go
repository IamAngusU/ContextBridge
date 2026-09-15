package systeminfo

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	OS              string    `json:"os"`
	OSVersion       string    `json:"os_version,omitempty"`
	Architecture    string    `json:"architecture"`
	CPU             string    `json:"cpu"`
	CPUCores        int       `json:"cpu_cores"`
	CPUFrequencyMHz int       `json:"cpu_frequency_mhz,omitempty"`
	CPUUtilization  int       `json:"cpu_utilization_percent,omitempty"`
	UptimeSeconds   uint64    `json:"uptime_seconds,omitempty"`
	MemoryTotal     uint64    `json:"memory_total_bytes"`
	MemoryAvailable uint64    `json:"memory_available_bytes"`
	MemoryType      string    `json:"memory_type,omitempty"`
	GPUs            []GPU     `json:"gpus"`
	Backends        []Backend `json:"backends"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type GPU struct {
	Name        string `json:"name"`
	Vendor      string `json:"vendor"`
	Backend     string `json:"backend"`
	MemoryTotal uint64 `json:"memory_total_bytes,omitempty"`
	MemoryUsed  uint64 `json:"memory_used_bytes,omitempty"`
	MemoryFree  uint64 `json:"memory_free_bytes,omitempty"`
	Driver      string `json:"driver,omitempty"`
	Temperature int    `json:"temperature_c,omitempty"`
	Utilization int    `json:"utilization_percent,omitempty"`
}

type Backend struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

func Detect(ctx context.Context) Snapshot {
	s := Snapshot{OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUCores: runtime.NumCPU(), UpdatedAt: time.Now().UTC()}
	if runtime.GOOS == "windows" {
		if details, ok := windowsDetails(ctx); ok {
			s.CPU = details.CPU
			s.CPUFrequencyMHz = details.CPUFrequencyMHz
			s.CPUUtilization = details.CPUUtilization
			s.OSVersion = details.OSVersion
			s.UptimeSeconds = details.UptimeSeconds
			s.MemoryTotal = details.MemoryTotal
			s.MemoryAvailable = details.MemoryAvailable
			s.MemoryType = memoryTypeLabel(details.MemoryType, details.MemorySpeed)
		}
	}
	if s.CPU == "" {
		s.CPU = cpuName(ctx)
	}
	if s.MemoryTotal == 0 {
		s.MemoryTotal, s.MemoryAvailable = memory(ctx)
	}
	if s.MemoryType == "" {
		s.MemoryType = memoryType(ctx)
	}
	if s.OSVersion == "" {
		s.OSVersion = osVersion(ctx)
	}
	if s.CPUFrequencyMHz == 0 {
		s.CPUFrequencyMHz = cpuFrequencyMHz(ctx)
	}
	if s.CPUUtilization == 0 {
		s.CPUUtilization = cpuUtilization()
	}
	if s.UptimeSeconds == 0 {
		s.UptimeSeconds = uptimeSeconds(ctx)
	}
	// Probe each vendor independently. A workstation can contain NVIDIA and
	// AMD devices at the same time; finding CUDA must not hide a ROCm rack.
	s.GPUs = append(s.GPUs, nvidiaGPUs(ctx)...)
	s.GPUs = append(s.GPUs, rocmGPUs(ctx)...)
	if runtime.GOOS == "darwin" {
		s.GPUs = append(s.GPUs, metalGPUs(ctx)...)
	}
	s.Backends = []Backend{
		backend("CUDA", "nvidia-smi"),
		backend("ROCm", "rocm-smi"),
		{Name: "Metal", Available: runtime.GOOS == "darwin", Detail: boolDetail(runtime.GOOS == "darwin", "Apple platform")},
		backend("Ollama", "ollama"),
		backendAny("llama.cpp", []string{"llama-server", "llama"}),
	}
	return s
}

type windowsSystemDetails struct {
	CPU             string `json:"cpu"`
	CPUFrequencyMHz int    `json:"cpu_frequency_mhz"`
	CPUUtilization  int    `json:"cpu_utilization"`
	OSVersion       string `json:"os_version"`
	UptimeSeconds   uint64 `json:"uptime_seconds"`
	MemoryTotal     uint64 `json:"memory_total"`
	MemoryAvailable uint64 `json:"memory_available"`
	MemoryType      int    `json:"memory_type"`
	MemorySpeed     int    `json:"memory_speed"`
}

func windowsDetails(ctx context.Context) (windowsSystemDetails, bool) {
	const script = `$p=Get-CimInstance Win32_Processor | Select-Object -First 1; $o=Get-CimInstance Win32_OperatingSystem; $m=Get-CimInstance Win32_PhysicalMemory | Select-Object -First 1; @{cpu=[string]$p.Name;cpu_frequency_mhz=[int]$p.MaxClockSpeed;cpu_utilization=[int]$p.LoadPercentage;os_version=([string]$o.Caption+' '+[string]$o.Version+' build '+[string]$o.BuildNumber);uptime_seconds=[uint64][Math]::Max(0,((Get-Date)-$o.LastBootUpTime).TotalSeconds);memory_total=[uint64]$o.TotalVisibleMemorySize*1024;memory_available=[uint64]$o.FreePhysicalMemory*1024;memory_type=[int]$m.SMBIOSMemoryType;memory_speed=[int]$m.ConfiguredClockSpeed}|ConvertTo-Json -Compress`
	raw, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return windowsSystemDetails{}, false
	}
	var value windowsSystemDetails
	if json.Unmarshal(raw, &value) != nil {
		return windowsSystemDetails{}, false
	}
	value.CPU = strings.TrimSpace(value.CPU)
	value.OSVersion = strings.TrimSpace(value.OSVersion)
	return value, value.CPU != ""
}

func backend(name, command string) Backend {
	path, err := exec.LookPath(command)
	return Backend{Name: name, Available: err == nil, Detail: path}
}

func backendAny(name string, commands []string) Backend {
	for _, command := range commands {
		if path, err := exec.LookPath(command); err == nil {
			return Backend{Name: name, Available: true, Detail: path}
		}
	}
	return Backend{Name: name}
}

func boolDetail(ok bool, detail string) string {
	if ok {
		return detail
	}
	return ""
}

func cpuName(ctx context.Context) string {
	if runtime.GOOS == "linux" {
		file, err := os.Open("/proc/cpuinfo")
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				parts := strings.SplitN(scanner.Text(), ":", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[0]) == "model name" {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	}
	var command *exec.Cmd
	if runtime.GOOS == "darwin" {
		command = exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
	} else if runtime.GOOS == "windows" {
		command = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_Processor | Select-Object -First 1 -ExpandProperty Name)")
	}
	if command != nil {
		if raw, err := command.Output(); err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	return runtime.GOARCH
}

func cpuFrequencyMHz(ctx context.Context) int {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); err == nil {
			value, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			if value > 0 {
				return int(value / 1000)
			}
		}
		if file, err := os.Open("/proc/cpuinfo"); err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				parts := strings.SplitN(scanner.Text(), ":", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[0]) == "cpu MHz" {
					value, _ := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
					return int(value + 0.5)
				}
			}
		}
	}
	if runtime.GOOS == "darwin" {
		raw, _ := exec.CommandContext(ctx, "sysctl", "-n", "hw.cpufrequency_max").Output()
		value, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		return int(value / 1000000)
	}
	return 0
}

func cpuUtilization() int {
	if runtime.GOOS != "linux" {
		return 0
	}
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 || runtime.NumCPU() == 0 {
		return 0
	}
	load, _ := strconv.ParseFloat(fields[0], 64)
	utilization := int(load/float64(runtime.NumCPU())*100 + 0.5)
	if utilization > 100 {
		return 100
	}
	return max(0, utilization)
}

func osVersion(ctx context.Context) string {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"'")
				}
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if raw, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + strings.TrimSpace(string(raw))
		}
	}
	return ""
}

func uptimeSeconds(ctx context.Context) uint64 {
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/uptime"); err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) > 0 {
				value, _ := strconv.ParseFloat(fields[0], 64)
				return uint64(value)
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if raw, err := exec.CommandContext(ctx, "sysctl", "-n", "kern.boottime").Output(); err == nil {
			text := string(raw)
			if index := strings.Index(text, "sec ="); index >= 0 {
				fields := strings.Fields(text[index+len("sec ="):])
				if len(fields) > 0 {
					boot, _ := strconv.ParseInt(strings.TrimRight(fields[0], ","), 10, 64)
					if boot > 0 && time.Now().Unix() > boot {
						return uint64(time.Now().Unix() - boot)
					}
				}
			}
		}
	}
	return 0
}

func memory(ctx context.Context) (uint64, uint64) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			values := map[string]uint64{}
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					value, _ := strconv.ParseUint(fields[1], 10, 64)
					values[strings.TrimSuffix(fields[0], ":")] = value * 1024
				}
			}
			return values["MemTotal"], values["MemAvailable"]
		}
	}
	if runtime.GOOS == "darwin" {
		raw, _ := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
		total, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		return total, 0
	}
	if runtime.GOOS == "windows" {
		raw, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", "$o=Get-CimInstance Win32_OperatingSystem; @{total=[uint64]$o.TotalVisibleMemorySize*1024;available=[uint64]$o.FreePhysicalMemory*1024}|ConvertTo-Json -Compress").Output()
		if err == nil {
			var value struct{ Total, Available uint64 }
			if json.Unmarshal(raw, &value) == nil {
				return value.Total, value.Available
			}
		}
	}
	return 0, 0
}

func memoryType(ctx context.Context) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	raw, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", "$m=Get-CimInstance Win32_PhysicalMemory | Select-Object -First 1 SMBIOSMemoryType,ConfiguredClockSpeed; @{type=[int]$m.SMBIOSMemoryType;speed=[int]$m.ConfiguredClockSpeed}|ConvertTo-Json -Compress").Output()
	if err != nil {
		return ""
	}
	var value struct {
		Type  int `json:"type"`
		Speed int `json:"speed"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return memoryTypeLabel(value.Type, value.Speed)
}

func memoryTypeLabel(memoryType, speed int) string {
	name := map[int]string{20: "DDR", 21: "DDR2", 24: "DDR3", 26: "DDR4", 30: "LPDDR4", 34: "DDR5", 35: "LPDDR5"}[memoryType]
	if name == "" {
		return ""
	}
	if speed > 0 {
		return fmt.Sprintf("%s @ %d MT/s", name, speed)
	}
	return name
}

func nvidiaGPUs(ctx context.Context) []GPU {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	raw, err := exec.CommandContext(ctx, path, "--query-gpu=name,memory.total,memory.used,memory.free,driver_version,temperature.gpu,utilization.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	return parseNVIDIAGPUs(raw)
}

func parseNVIDIAGPUs(raw []byte) []GPU {
	var result []GPU
	reader := csv.NewReader(strings.NewReader(string(raw)))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	for {
		parts, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil
		}
		if len(parts) < 7 {
			continue
		}
		temperature, _ := strconv.Atoi(strings.TrimSpace(parts[5]))
		utilization, _ := strconv.Atoi(strings.TrimSpace(parts[6]))
		result = append(result, GPU{Name: strings.TrimSpace(parts[0]), Vendor: "NVIDIA", Backend: "CUDA", MemoryTotal: mib(parts[1]), MemoryUsed: mib(parts[2]), MemoryFree: mib(parts[3]), Driver: strings.TrimSpace(parts[4]), Temperature: temperature, Utilization: utilization})
	}
	return result
}

func rocmGPUs(ctx context.Context) []GPU {
	path, err := exec.LookPath("rocm-smi")
	if err != nil {
		return nil
	}
	raw, err := exec.CommandContext(ctx, path, "--showproductname", "--showmeminfo", "vram", "--json").Output()
	if err != nil {
		return nil
	}
	return parseROCmGPUs(raw)
}

func parseROCmGPUs(raw []byte) []GPU {
	var cards map[string]map[string]interface{}
	if json.Unmarshal(raw, &cards) != nil {
		return nil
	}
	result := make([]GPU, 0, len(cards))
	keys := make([]string, 0, len(cards))
	for key := range cards {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		card := cards[key]
		name := stringValue(card, "Card series", "Card model", "Card SKU")
		total := numberValue(card, "VRAM Total Memory (B)")
		used := numberValue(card, "VRAM Total Used Memory (B)")
		result = append(result, GPU{Name: name, Vendor: "AMD", Backend: "ROCm", MemoryTotal: total, MemoryUsed: used, MemoryFree: subtract(total, used)})
	}
	return result
}

func metalGPUs(ctx context.Context) []GPU {
	raw, err := exec.CommandContext(ctx, "system_profiler", "SPDisplaysDataType", "-json").Output()
	if err != nil {
		return nil
	}
	var payload struct {
		Displays []map[string]interface{} `json:"SPDisplaysDataType"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	result := make([]GPU, 0, len(payload.Displays))
	for _, display := range payload.Displays {
		name, _ := display["sppci_model"].(string)
		result = append(result, GPU{Name: name, Vendor: "Apple", Backend: "Metal"})
	}
	return result
}

func mib(value string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return n * 1024 * 1024
}

func stringValue(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return "AMD GPU"
}

func numberValue(values map[string]interface{}, key string) uint64 {
	value := fmt.Sprint(values[key])
	n, _ := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return n
}

func subtract(total, used uint64) uint64 {
	if used > total {
		return 0
	}
	return total - used
}
