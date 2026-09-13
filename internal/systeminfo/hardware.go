package systeminfo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	OS              string    `json:"os"`
	Architecture    string    `json:"architecture"`
	CPU             string    `json:"cpu"`
	CPUCores        int       `json:"cpu_cores"`
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
	s.CPU = cpuName(ctx)
	s.MemoryTotal, s.MemoryAvailable = memory(ctx)
	s.MemoryType = memoryType(ctx)
	s.GPUs = append(s.GPUs, nvidiaGPUs(ctx)...)
	if len(s.GPUs) == 0 {
		s.GPUs = append(s.GPUs, rocmGPUs(ctx)...)
	}
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
	name := map[int]string{20: "DDR", 21: "DDR2", 24: "DDR3", 26: "DDR4", 30: "LPDDR4", 34: "DDR5", 35: "LPDDR5"}[value.Type]
	if name == "" {
		return ""
	}
	if value.Speed > 0 {
		return fmt.Sprintf("%s @ %d MT/s", name, value.Speed)
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
	var result []GPU
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.Split(line, ",")
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
	var cards map[string]map[string]interface{}
	if json.Unmarshal(raw, &cards) != nil {
		return nil
	}
	result := make([]GPU, 0, len(cards))
	for _, card := range cards {
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
