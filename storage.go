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
		NonIntrusiveMode:         false,
		PreferWorkerForWireless:  true,
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
	// First attempt to decode an array of profiles (new format)
	var profiles []DeviceProfile
	if json.Unmarshal(b, &profiles) == nil && len(profiles) > 0 {
		// Backfill any missing metadata for legacy profiles so the
		// reconnect logic can match by VID/PID/interface and so
		// auto-profiling doesn't need to re-discover already-known
		// devices. If we successfully enrich any profile, write the
		// updated array back to disk.
		modified := false
		for i := range profiles {
			p := &profiles[i]
			if p.Path == "" {
				continue
			}
			// If vendor/product/interface/mode are missing, attempt to
			// look up the live DeviceInfo for this path and fill them.
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
					// mark unknown mode to avoid repeatedly probing this
					// stale path unnecessarily
					if p.Mode == "" {
						p.Mode = "unknown"
						modified = true
					}
				}
			}
		}
		cachedProfiles = profiles
		if modified {
			// Persist the upgraded profile array so future runs can
			// take advantage of VID/PID matching without re-probing.
			fileMu.Lock()
			_ = os.WriteFile(cacheFile, mustJSON(cachedProfiles), 0644)
			fileMu.Unlock()
		}
		return
	}
	// Backwards compatibility: try a single profile
	var p DeviceProfile
	if json.Unmarshal(b, &p) == nil && p.Path != "" {
		cachedProfiles = []DeviceProfile{p}
	}
}

func saveConnProfile(p DeviceProfile) {
	// Update existing entry for same path or append
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
