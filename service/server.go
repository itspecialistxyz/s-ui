package service

import (
	"encoding/base64"
	"os"
	"runtime"
	"s-ui/config"
	"s-ui/logger"
	"s-ui/util/common"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type ServerService struct{}

func (s *ServerService) GetStatus(request string) *map[string]interface{} {
	status := make(map[string]interface{})
	requests := strings.Split(request, ",")
	for _, req := range requests {
		switch req {
		case "cpu":
			if val, err := s.GetCpuPercent(); err == nil {
				status["cpu"] = val
			} else {
				logger.Warningf("failed to get cpu percent for status: %v", err)
			}
		case "mem":
			if val, err := s.GetMemInfo(); err == nil {
				status["mem"] = val
			} else {
				logger.Warningf("failed to get mem info for status: %v", err)
			}
		case "net":
			if val, err := s.GetNetInfo(); err == nil {
				status["net"] = val
			} else {
				logger.Warningf("failed to get net info for status: %v", err)
			}
		case "sys":
			if val, err := s.GetUptime(); err == nil {
				status["uptime"] = val
			} else {
				logger.Warningf("failed to get uptime for status: %v", err)
			}
			// GetSystemInfo itself handles its internal errors by logging and returning partial data
			status["sys"] = s.GetSystemInfo()
		case "sbd":
			status["sbd"] = s.GetSingboxInfo()
		}
	}
	return &status
}

func (s *ServerService) GetCpuPercent() (float64, error) {
	percents, err := cpu.Percent(0, false)
	if err != nil {
		return 0, common.NewErrorf("get cpu percent failed: %w", err)
	}
	if len(percents) == 0 {
		return 0, common.NewError("cpu.Percent returned empty slice")
	}
	return percents[0], nil
}

func (s *ServerService) GetUptime() (uint64, error) {
	upTime, err := host.Uptime()
	if err != nil {
		return 0, common.NewErrorf("get uptime failed: %w", err)
	}
	return upTime, nil
}

func (s *ServerService) GetMemInfo() (map[string]interface{}, error) {
	memInfoStat, err := mem.VirtualMemory()
	if err != nil {
		return nil, common.NewErrorf("get virtual memory failed: %w", err)
	}
	info := make(map[string]interface{})
	info["current"] = memInfoStat.Used
	info["total"] = memInfoStat.Total
	return info, nil
}

func (s *ServerService) GetNetInfo() (map[string]interface{}, error) {
	ioStats, err := net.IOCounters(false)
	if err != nil {
		return nil, common.NewErrorf("get io counters failed: %w", err)
	}
	if len(ioStats) == 0 {
		return nil, common.NewError("net.IOCounters returned empty slice")
	}
	info := make(map[string]interface{})
	ioStat := ioStats[0]
	info["sent"] = ioStat.BytesSent
	info["recv"] = ioStat.BytesRecv
	info["psent"] = ioStat.PacketsSent
	info["precv"] = ioStat.PacketsRecv
	return info, nil
}

func (s *ServerService) GetSingboxInfo() map[string]interface{} {
	var rtm runtime.MemStats
	runtime.ReadMemStats(&rtm)
	isRunning := corePtr.IsRunning()
	uptime := uint32(0)
	if isRunning {
		uptime = corePtr.GetInstance().Uptime()
	}
	return map[string]interface{}{
		"running": isRunning,
		"stats": map[string]interface{}{
			"NumGoroutine": uint32(runtime.NumGoroutine()),
			"Alloc":        rtm.Alloc,
			"Uptime":       uptime,
		},
	}
}

func (s *ServerService) GetSystemInfo() map[string]interface{} {
	info := make(map[string]interface{}, 0)
	var rtm runtime.MemStats
	runtime.ReadMemStats(&rtm)

	info["appMem"] = rtm.Sys
	info["appThreads"] = uint32(runtime.NumGoroutine())
	cpuInfoSlice, err := cpu.Info()
	if err != nil {
		logger.Warningf("failed to get CPU info: %v", err)
	} else if len(cpuInfoSlice) > 0 {
		info["cpuType"] = cpuInfoSlice[0].ModelName
	} else {
		logger.Warning("cpu.Info() returned empty slice")
	}

	info["cpuCount"] = runtime.NumCPU()
	hostname, err := os.Hostname()
	if err != nil {
		logger.Warningf("failed to get hostname: %v", err)
		info["hostName"] = "unknown"
	} else {
		info["hostName"] = hostname
	}
	info["appVersion"] = config.GetVersion()
	ipv4 := make([]string, 0)
	ipv6 := make([]string, 0)

	netInterfaces, err := net.Interfaces()
	if err != nil {
		logger.Warningf("failed to get network interfaces: %v", err)
	} else {
		for _, iface := range netInterfaces {
			// Check if interface is up and not loopback
			isUp := false
			isLoopback := false
			// Correctly check flags by iterating over them
			for _, flagStr := range iface.Flags {
				if flagStr == "up" {
					isUp = true
				}
				if flagStr == "loopback" {
					isLoopback = true
				}
			}

			if isUp && !isLoopback {
				// Correctly access Addrs as a slice
				for _, address := range iface.Addrs {
					// address.Addr is in format like "192.168.1.1/24" or "fe80::1/64"
					ipStr := address.Addr
					if idx := strings.Index(ipStr, "/"); idx != -1 {
						ipStr = ipStr[:idx] // remove CIDR mask
					}
					if strings.Contains(ipStr, ".") {
						ipv4 = append(ipv4, ipStr)
					} else if strings.Contains(ipStr, ":") && !strings.HasPrefix(ipStr, "fe80::") {
						ipv6 = append(ipv6, ipStr)
					}
				}
			}
		}
	}
	info["ipv4"] = ipv4
	info["ipv6"] = ipv6

	return info
}

func (s *ServerService) GetLogs(countStr string, level string) ([]string, error) { // Changed to return error
	c, err := strconv.Atoi(countStr)
	if err != nil {
		// Return error instead of defaulting, or make default explicit and clear
		return nil, common.NewErrorf("invalid count parameter '%s': %w", countStr, err)
	}
	if c <= 0 {
		// Consider if this should be an error or return empty logs
		return []string{}, nil // Or perhaps an error: common.NewError("count must be positive")
	}
	return logger.GetLogs(c, level), nil
}

func (s *ServerService) GenKeypair(keyType string, options string) ([]string, error) { // Changed to return error
	if len(keyType) == 0 {
		return nil, common.NewError("No keypair type specified to generate")
	}

	switch keyType {
	case "ech":
		return s.generateECHKeyPair(options)
	case "tls":
		return s.generateTLSKeyPair(options)
	case "reality":
		return s.generateRealityKeyPair()
	case "wireguard":
		return s.generateWireGuardKey(options)
	}

	return nil, common.NewErrorf("Failed to generate keypair: unknown type %s", keyType)
}

func (s *ServerService) generateECHKeyPair(options string) ([]string, error) { // Changed to return error
	parts := strings.Split(options, ",")
	if len(parts) != 2 {
		return nil, common.NewErrorf("Failed to generate ECH keypair: invalid options format, expected 'domain,isLite (true/false)' got '%s'", options)
	}
	isLite, err := strconv.ParseBool(parts[1])
	if err != nil {
		return nil, common.NewErrorf("Failed to generate ECH keypair: invalid boolean for isLite '%s': %w", parts[1], err)
	}
	configPem, keyPem, err := tls.ECHKeygenDefault(parts[0], isLite)
	if err != nil {
		return nil, common.NewErrorf("Failed to generate ECH keypair: %w", err)
	}
	// Return keys as separate elements in the slice, not split by newline, for easier programmatic use.
	// If newline splitting is truly desired by client, it can do it.
	return []string{configPem, keyPem}, nil
}

func (s *ServerService) generateTLSKeyPair(serverName string) ([]string, error) { // Changed to return error
	if serverName == "" {
		return nil, common.NewError("server name cannot be empty for TLS keypair generation")
	}
	privateKeyPem, publicKeyPem, err := tls.GenerateCertificate(nil, nil, time.Now, serverName, time.Now().AddDate(1, 0, 0)) // 1 year validity
	if err != nil {
		return nil, common.NewErrorf("Failed to generate TLS keypair: %w", err)
	}
	return []string{string(privateKeyPem), string(publicKeyPem)}, nil
}

func (s *ServerService) generateRealityKeyPair() ([]string, error) { // Changed to return error
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, common.NewErrorf("Failed to generate Reality private key: %w", err)
	}
	publicKey := privateKey.PublicKey()
	// Return keys in a structured way or clearly labeled if string format is kept
	return []string{
		base64.RawURLEncoding.EncodeToString(privateKey[:]),
		base64.RawURLEncoding.EncodeToString(publicKey[:]),
	}, nil
}

func (s *ServerService) generateWireGuardKey(pk string) ([]string, error) { // Changed to return error
	if len(pk) > 0 {
		parsedKey, err := wgtypes.ParseKey(pk)
		if err != nil {
			return nil, common.NewErrorf("Failed to parse provided WireGuard private key: %w", err)
		}
		return []string{parsedKey.PublicKey().String()}, nil
	}
	wgPrivateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, common.NewErrorf("Failed to generate WireGuard keypair: %w", err)
	}
	return []string{wgPrivateKey.String(), wgPrivateKey.PublicKey().String()}, nil
}
