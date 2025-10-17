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
	if err := json.Unmarshal(data, &cd); err == nil {
		lastChargeTime = cd.LastChargeTime
		lastChargeLevel = cd.LastChargeLevel

		if cd.Timestamp != "" {
			if savedTime, err := time.Parse(time.RFC3339, cd.Timestamp); err == nil {
				if time.Since(savedTime) < 24*time.Hour {
					dischargeRate = cd.DischargeRate
					chargeRate = cd.ChargeRate
				}
			}
		}
	}
}

func saveChargeData() {
	cd := ChargeData{
		LastChargeTime:  lastChargeTime,
		LastChargeLevel: lastChargeLevel,
		DischargeRate:   dischargeRate,
		ChargeRate:      chargeRate,
		Timestamp:       time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(cd, "", "  ")
	if err != nil {
		return
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	_ = os.WriteFile(dataFile, data, 0644)
}

func loadSettings() {
	settings = Settings{
		StartWithWindows:         false,
		StartMinimized:           false,
		RefreshInterval:          5,
		NotificationsEnabled:     false,
		LowBatteryThreshold:      20,
		CriticalBatteryThreshold: 10,
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
	var p DeviceProfile
	if json.Unmarshal(b, &p) == nil && p.Path != "" {
		cachedProfile = &p
	}
}

func saveConnProfile(p DeviceProfile) {
	cachedProfile = &p
	b := mustJSON(p)
	fileMu.Lock()
	_ = os.WriteFile(cacheFile, b, 0644)
	fileMu.Unlock()
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
