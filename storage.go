//go:build windows
// +build windows

package main

import (
	"encoding/json"
	"os"
	"time"
)

func loadChargeData() {
	data, err := os.ReadFile(dataFile)
	if err != nil {
		return
	}
	var cd ChargeData
	if err := json.Unmarshal(data, &cd); err != nil {
		return
	}

	if cd.LastChargeTime != "" {
		lastChargeTime = cd.LastChargeTime
	}
	if cd.LastChargeLevel > 0 {
		lastChargeLevel = cd.LastChargeLevel
	}
	if len(cd.Devices) > 0 && batteryEstimator != nil {
		batteryEstimator.Restore(cd.Devices)
	}
	if len(cd.HistorySamples) > 0 || len(cd.HistoryEvents) > 0 {
		loadHistoryFromChargeData(cd.HistorySamples, cd.HistoryEvents)
	}
}

func saveChargeData() {
	snapshot := make(map[string]PersistedDeviceModel)
	if batteryEstimator != nil {
		snapshot = batteryEstimator.Snapshot()
	}

	samples, events := getHistorySnapshot()

	cd := ChargeData{
		LastChargeTime:  lastChargeTime,
		LastChargeLevel: lastChargeLevel,
		Devices:         snapshot,
		Timestamp:       time.Now().Format(time.RFC3339),
		LastLevel:       batteryLvl,
		LastLevelTime:   time.Now().Format(time.RFC3339),
		LastCharging:    isCharging,
		HistorySamples:  samples,
		HistoryEvents:   events,
	}
	data, err := json.MarshalIndent(cd, "", "  ")
	if err != nil {
		return
	}
	fileMu.Lock()
	_ = os.WriteFile(dataFile, data, 0644)
	fileMu.Unlock()
}

func loadSettings() {
	settings = Settings{
		StartWithWindows:         false,
		StartMinimized:           false,
		RefreshInterval:          5,
		NotificationsEnabled:     false,
		NonIntrusiveMode:         false,
		PreferWorkerForWireless:  true,
		LowBatteryThreshold:      20,
		CriticalBatteryThreshold: 10,
		SafeMode:                 true,
		ShowPercentageOnIcon:     false,
	}
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return
	}
	json.Unmarshal(data, &settings)
}

func saveSettings() {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	fileMu.Lock()
	_ = os.WriteFile(settingsFile, data, 0644)
	fileMu.Unlock()
	if settings.StartWithWindows {
		enableStartup()
	} else {
		disableStartup()
	}
}

func loadConnProfile() {
	b, err := os.ReadFile(cacheFile)
	if err != nil || len(b) == 0 {
		return
	}
	var profiles []DeviceProfile
	if json.Unmarshal(b, &profiles) == nil && len(profiles) > 0 {
		modified := false
		for i := range profiles {
			p := &profiles[i]
			if p.Path == "" {
				continue
			}
			if p.VendorID == 0 || p.ProductID == 0 || p.InterfaceNbr == 0 || p.Mode == "" {
				if info := findDeviceInfoByPath(p.Path); info != nil {
					if p.VendorID == 0 {
						p.VendorID = info.VendorID
					}
					if p.ProductID == 0 {
						p.ProductID = info.ProductID
					}
					if p.InterfaceNbr == 0 {
						p.InterfaceNbr = info.InterfaceNbr
					}
					if p.Mode == "" {
						if qualifiesForWorkerManaged(info) {
							p.Mode = "wireless"
						} else {
							p.Mode = "wired"
						}
					}
					modified = true
				} else {
					if p.Mode == "" {
						p.Mode = "unknown"
						modified = true
					}
				}
			}
		}
		cachedProfiles = profiles
		if modified {
			fileMu.Lock()
			_ = os.WriteFile(cacheFile, mustJSON(cachedProfiles), 0644)
			fileMu.Unlock()
		}
		return
	}
	var p DeviceProfile
	if json.Unmarshal(b, &p) == nil && p.Path != "" {
		cachedProfiles = []DeviceProfile{p}
	}
}

func saveConnProfile(p DeviceProfile) {
	updated := false
	for i := range cachedProfiles {
		if cachedProfiles[i].Path == p.Path {
			cachedProfiles[i] = p
			updated = true
			break
		}
	}
	if !updated {
		cachedProfiles = append(cachedProfiles, p)
	}
	b := mustJSON(cachedProfiles)
	fileMu.Lock()
	_ = os.WriteFile(cacheFile, b, 0644)
	fileMu.Unlock()
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
