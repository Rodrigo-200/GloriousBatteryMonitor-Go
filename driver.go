//go:build windows
// +build windows

package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sstallion/go-hid"
)

var knownDevices = map[uint16]map[uint16]string{
	0x258a: {
		0x2009: "Model O2 Wired",
		0x200b: "Model O2 Wireless",
		0x2011: "Model O Wired",
		0x2012: "Model D Wired",
		0x2013: "Model O Wireless",
		0x2014: "Model I2 Wired",
		0x2015: "Model D- Wired",
		0x2016: "Model I2 Wireless",
		0x2017: "Model O Pro Wired",
		0x2018: "Model O Pro Wireless",
		0x2019: "Model O- Wired",
		0x2023: "Model D Wireless",
		0x2024: "Model O- Wireless",
		0x2025: "Model D- Wireless",
		0x2031: "Model D2 Wired",
		0x2033: "Model D2 Wireless",
		0x2036: "Model I Wired",
		0x2037: "Model I Wireless",
		0x2046: "Model I Wireless Receiver",
	},
	0x093A: {
		0x824D: "Model D 2 Wireless",
	},
}

type MouseDevice struct {
	handle     *hid.Device
	path       string
	modelName  string
	isWireless bool
	vendorID   uint16
	productID  uint16
	release    uint16
}

func deviceKeyFromInfo(info *hid.DeviceInfo, isWireless bool) DeviceKey {
	if info == nil {
		return DeviceKey{}
	}
	return DeviceKey{
		VendorID:  info.VendorID,
		ProductID: info.ProductID,
		Release:   info.ReleaseNbr,
		Wireless:  isWireless,
	}
}

func deviceKeyFromMouse(m *MouseDevice) DeviceKey {
	if m == nil {
		return DeviceKey{}
	}
	return DeviceKey{
		VendorID:  m.vendorID,
		ProductID: m.productID,
		Release:   m.release,
		Wireless:  m.isWireless,
	}
}

func recordBatteryEstimate(level int, charging bool, mouse *MouseDevice) BatteryEstimate {
	if batteryEstimator == nil {
		return BatteryEstimate{}
	}
	key := currentDeviceKey
	if key == (DeviceKey{}) {
		if mouse != nil {
			key = deviceKeyFromMouse(mouse)
			currentDeviceKey = key
		} else if currentHIDPath != "" {
			if info := findDeviceInfoByPath(currentHIDPath); info != nil {
				key = deviceKeyFromInfo(info, isWirelessInterface(info.Path))
				currentDeviceKey = key
			}
		}
	}
	if key == (DeviceKey{}) {
		return BatteryEstimate{}
	}
	return batteryEstimator.RecordSample(key, level, charging, time.Now())
}

func persistEstimator(est BatteryEstimate) {
	if !est.Valid || est.Samples < 3 {
		return
	}
	etaPersistMu.Lock()
	if !etaLastPersist.IsZero() && time.Since(etaLastPersist) < 2*time.Minute {
		etaPersistMu.Unlock()
		return
	}
	etaLastPersist = time.Now()
	etaPersistMu.Unlock()
	saveChargeData()
}

func formatHours(hours float64) string {
	if hours <= 0 {
		return ""
	}
	totalMinutes := int(hours*60 + 0.5)
	if totalMinutes <= 0 {
		return "<1m"
	}
	days := totalMinutes / (60 * 24)
	if days > 0 {
		remaining := totalMinutes - days*24*60
		hoursPart := remaining / 60
		if hoursPart > 0 {
			return fmt.Sprintf("%dd %dh", days, hoursPart)
		}
		return fmt.Sprintf("%dd", days)
	}
	hoursPart := totalMinutes / 60
	minutesPart := totalMinutes % 60
	if hoursPart > 0 {
		if minutesPart > 0 {
			return fmt.Sprintf("%dh %dm", hoursPart, minutesPart)
		}
		return fmt.Sprintf("%dh", hoursPart)
	}
	return fmt.Sprintf("%dm", minutesPart)
}

func buildEtaPayload(est BatteryEstimate, level int, charging bool) map[string]interface{} {
	if est.Samples == 0 && !est.Paused {
		return nil
	}
	payload := map[string]interface{}{
		"paused":      est.Paused,
		"confidence":  int(est.Confidence*100 + 0.5),
		"samples":     est.Samples,
		"phase":       string(est.Phase),
		"generatedAt": est.GeneratedAt.Format(time.RFC3339),
		"rate":        est.RawRate,
		"level":       level,
		"charging":    charging,
	}
	if est.Mode != "" {
		payload["mode"] = est.Mode
	}
	if !est.Paused && est.Valid {
		valueMinutes := int(est.Hours*60 + 0.5)
		lowerMinutes := int(est.Lower*60 + 0.5)
		upperMinutes := int(est.Upper*60 + 0.5)
		payload["value"] = formatHours(est.Hours)
		payload["valueMinutes"] = valueMinutes
		payload["lowerMinutes"] = lowerMinutes
		payload["upperMinutes"] = upperMinutes
		payload["lower"] = formatHours(est.Lower)
		payload["upper"] = formatHours(est.Upper)
	} else if est.Paused {
		payload["value"] = "Paused"
	}
	return payload
}

func recordEstimateForPath(path string, level int, charging bool) BatteryEstimate {
	if batteryEstimator == nil {
		return BatteryEstimate{}
	}
	key := currentDeviceKey
	if key == (DeviceKey{}) && path != "" {
		if info := findDeviceInfoByPath(path); info != nil {
			key = deviceKeyFromInfo(info, isWirelessInterface(info.Path))
			currentDeviceKey = key
		}
	}
	if key == (DeviceKey{}) {
		return BatteryEstimate{}
	}
	return batteryEstimator.RecordSample(key, level, charging, time.Now())
}

func computeEtaForPath(path string, level int, charging bool, actual bool) (BatteryEstimate, map[string]interface{}) {
	if !actual {
		return BatteryEstimate{}, nil
	}
	est := recordEstimateForPath(path, level, charging)
	if est.Valid {
		persistEstimator(est)
	}
	return est, buildEtaPayload(est, level, charging)
}

var (
	currentMouse *MouseDevice
	mouseMutex   sync.Mutex
	failCount    int
)

func initDriver() { hid.Init() }

func parseBattery(data []byte) (int, bool, bool) {
	if len(data) < 9 {
		return -1, false, false
	}
	if data[6] == 0x83 {
		level := int(data[8])
		charging := data[7] == 0x01
		if level >= 0 && level <= 100 {
			return level, charging, true
		}
	}
	for i := 0; i < len(data)-2; i++ {
		if data[i] == 0x83 && i+2 < len(data) {
			level := int(data[i+2])
			charging := data[i+1] == 0x01
			if level >= 0 && level <= 100 {
				if logger != nil {
					logger.Printf("[PARSE] Found battery at offset %d: level=%d charging=%v", i, level, charging)
				}
				return level, charging, true
			}
		}
	}
	return -1, false, false
}

func parseModelD2WirelessBattery(data []byte) (int, bool, bool) {
	if len(data) < 8 {
		return -1, false, false
	}

	// Try common Model D 2 Wireless report patterns
	// Pattern 1: Battery at offset 2, charging at offset 3
	if len(data) >= 4 {
		level := int(data[2])
		charging := data[3] == 0x01 || data[3] == 0x02
		if level >= 0 && level <= 100 {
			return level, charging, true
		}
	}

	// Pattern 2: Battery at offset 3, charging flag at offset 4
	if len(data) >= 5 {
		level := int(data[3])
		charging := data[4] == 0x01 || data[4] == 0x02
		if level >= 0 && level <= 100 {
			return level, charging, true
		}
	}

	// Pattern 3: Look for battery percentage in valid range anywhere in report
	for i := 0; i < len(data)-1; i++ {
		level := int(data[i])
		if level >= 0 && level <= 100 {
			// Check if next byte could be charging flag
			if i+1 < len(data) {
				charging := data[i+1] == 0x01 || data[i+1] == 0x02
				return level, charging, true
			}
		}
	}

	return -1, false, false
}

func isModelD2Wireless(info *hid.DeviceInfo) bool {
	return info.VendorID == 0x093A && info.ProductID == 0x824D
}

func isWirelessInterface(path string) bool {
	return strings.Contains(strings.ToLower(path), "mi_02")
}

func findAndConnectMouse() (*MouseDevice, error) {
	var candidates []hid.DeviceInfo
	for _, vid := range gloriousVendorIDs {
		hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
			if isKeyboardInterface(info) {
				return nil
			}
			if settings.SafeMode && shouldSkipDeviceInSafeMode(info) {
				if logger != nil {
					logger.Printf("[SAFE_MODE] Skipping device VID:0x%04X PID:0x%04X UsagePage:0x%04X", info.VendorID, info.ProductID, info.UsagePage)
				}
				return nil
			}
			candidates = append(candidates, *info)
			return nil
		})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no mouse found")
	}

	for _, info := range candidates {
		dev, err := safeOpenPath(info.Path)
		if err != nil {
			if logger != nil {
				logger.Printf("[DRIVER] Failed to open device: %v", err)
			}
			continue
		}

		// For Model D 2 Wireless, use read-only Input reports approach
		if isModelD2Wireless(&info) {
			// D2W detected

			// Try to read Input reports directly without sending Feature commands
			buf := make([]byte, 65)

			// Try to read multiple times to get a battery report
			for attempt := 0; attempt < 5; attempt++ {
				n, err := dev.Read(buf)
				if err == nil && n > 0 {
					level, charging, valid := parseModelD2WirelessBattery(buf[:n])
					if valid {
						modelName := knownDevices[info.VendorID][info.ProductID]
						mouse := &MouseDevice{
							handle:     dev,
							path:       info.Path,
							modelName:  modelName,
							isWireless: isWirelessInterface(info.Path),
							vendorID:   info.VendorID,
							productID:  info.ProductID,
							release:    info.ReleaseNbr,
						}
						currentDeviceKey = deviceKeyFromMouse(mouse)
						if logger != nil {
							logger.Printf("[D2W] Connected via Input reports: %s (wireless:%v) %d%% charging:%v", mouse.modelName, mouse.isWireless, level, charging)
						}
						return mouse, nil
					}
				}
				time.Sleep(100 * time.Millisecond)
			}

			// If Input reports don't work, try a minimal Feature report read (read-only)
			buf = make([]byte, 65)
			n, err := dev.GetFeatureReport(buf)
			if err == nil && n > 0 {
				level, charging, valid := parseModelD2WirelessBattery(buf[:n])
				if valid {
					modelName := knownDevices[info.VendorID][info.ProductID]
					mouse := &MouseDevice{
						handle:     dev,
						path:       info.Path,
						modelName:  modelName,
						isWireless: isWirelessInterface(info.Path),
						vendorID:   info.VendorID,
						productID:  info.ProductID,
						release:    info.ReleaseNbr,
					}
					currentDeviceKey = deviceKeyFromMouse(mouse)
					if logger != nil {
						logger.Printf("[D2W] Connected via Feature report: %s (wireless:%v) %d%% charging=%v", mouse.modelName, mouse.isWireless, level, charging)
					}
					return mouse, nil
				}
			}

			if logger != nil {
				logger.Printf("[D2W] Failed to read battery data from Model D 2 Wireless")
			}
			dev.Close()
			continue
		}

		// Standard handling for other devices
		canWrite := canWriteToDevice(&info)
		if !canWrite {
			logBlockedWrite(info.Path, "SafeMode enabled or device not whitelisted")
			dev.Close()
			continue
		}

		cmd := make([]byte, 65)
		cmd[3], cmd[4], cmd[6] = 0x02, 0x02, 0x83
		if _, err := dev.SendFeatureReport(cmd); err != nil {
			if logger != nil {
				logger.Printf("[DRIVER] SendFeatureReport failed: %v", err)
			}
			dev.Close()
			continue
		}
		time.Sleep(50 * time.Millisecond)

		buf := make([]byte, 65)
		n, _ := dev.GetFeatureReport(buf)
		if n <= 0 {
			dev.Close()
			continue
		}
		level, charging, valid := parseBattery(buf[:n])
		if valid {
			modelName := knownDevices[info.VendorID][info.ProductID]
			if modelName == "" {
				modelName = fmt.Sprintf("Unknown (0x%04X:0x%04X)", info.VendorID, info.ProductID)
			}
			mouse := &MouseDevice{
				handle:     dev,
				path:       info.Path,
				modelName:  modelName,
				isWireless: isWirelessInterface(info.Path),
				vendorID:   info.VendorID,
				productID:  info.ProductID,
				release:    info.ReleaseNbr,
			}
			currentDeviceKey = deviceKeyFromMouse(mouse)
			if logger != nil {
				logger.Printf("[DRIVER] Connected: %s (wireless:%v) %d%% charging:%v", mouse.modelName, mouse.isWireless, level, charging)
			}
			return mouse, nil
		}
		dev.Close()
	}
	return nil, fmt.Errorf("no working mouse")
}

func (m *MouseDevice) ReadBattery() (int, bool, error) {
	if m.handle == nil {
		return -1, false, fmt.Errorf("not connected")
	}

	// Check if this is a Model D 2 Wireless device
	info := findDeviceInfoByPath(m.path)
	if info != nil && isModelD2Wireless(info) {
		return m.readModelD2WirelessBattery()
	}

	// Standard handling for other devices
	cmd := make([]byte, 65)
	cmd[3], cmd[4], cmd[6] = 0x02, 0x02, 0x83
	if _, err := m.handle.SendFeatureReport(cmd); err != nil {
		if logger != nil {
			logger.Printf("[READ] SendFeatureReport failed: %v", err)
		}
		return -1, false, err
	}
	time.Sleep(50 * time.Millisecond)

	buf := make([]byte, 65)
	n, err := m.handle.GetFeatureReport(buf)
	if err != nil || n <= 0 {
		if logger != nil {
			logger.Printf("[READ] GetFeatureReport failed: n=%d err=%v", n, err)
		}
		return -1, false, fmt.Errorf("read failed")
	}
	if logger != nil {
		if n > 0 {
			logger.Printf("[READ] Received %d bytes: %02x", n, buf[:min(n, 16)])
		} else {
			logger.Printf("[READ] Received %d bytes (invalid)", n)
		}
	}
	if n <= 0 {
		return 0, false, fmt.Errorf("invalid byte count: %d", n)
	}

	level, charging, valid := parseBattery(buf[:n])
	if !valid {
		if logger != nil {
			logger.Printf("[READ] parseBattery returned invalid")
		}
		return -1, false, fmt.Errorf("invalid")
	}
	if logger != nil {
		logger.Printf("[READ] Battery: %d%% charging=%v", level, charging)
	}
	return level, charging, nil
}

func (m *MouseDevice) readModelD2WirelessBattery() (int, bool, error) {
	if logger != nil {
		logger.Printf("[D2W] Reading battery from Model D 2 Wireless")
	}

	// Try Input reports first (read-only approach)
	buf := make([]byte, 65)

	// Try to read multiple times to get a battery report
	for attempt := 0; attempt < 3; attempt++ {
		n, err := m.handle.Read(buf)
		if err == nil && n > 0 {
			level, charging, valid := parseModelD2WirelessBattery(buf[:n])
			if valid {
				if logger != nil {
					logger.Printf("[D2W] Battery via Input: %d%% charging=%v", level, charging)
				}
				return level, charging, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Fallback to read-only Feature report
	buf = make([]byte, 65)
	n, err := m.handle.GetFeatureReport(buf)
	if err == nil && n > 0 {
		level, charging, valid := parseModelD2WirelessBattery(buf[:n])
		if valid {
			if logger != nil {
				logger.Printf("[D2W] Battery via Feature: %d%% charging=%v", level, charging)
			}
			return level, charging, nil
		}
	}

	if logger != nil {
		logger.Printf("[D2W] Failed to read battery: n=%d err=%v", n, err)
	}
	return -1, false, fmt.Errorf("D2W read failed")
}

func (m *MouseDevice) Close() {
	if m.handle != nil {
		m.handle.Close()
		m.handle = nil
	}
}

func reconnect() {
	mouseMutex.Lock()
	defer mouseMutex.Unlock()

	mouse := currentMouse
	if mouse != nil {
		level, charging, err := mouse.ReadBattery()
		if err == nil {
			failCount = 0

			if wasCharging && !charging && level > 0 {
				if level >= lastChargeLevel || level >= 10 {
					lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
					lastChargeLevel = level
					saveChargeData()
					if logger != nil {
						logger.Printf("[CHARGE] saved charge completion: time=%s level=%d (wasCharging=%v charging=%v)", lastChargeTime, lastChargeLevel, wasCharging, charging)
					}
				}
			}

			batteryLvl = level
			isCharging = charging
			deviceModelName = mouse.modelName
			linkDown = false
			wasCharging = charging

			lastKnownMu.Lock()
			showLastKnown = false
			lastKnownLevel = level
			lastKnownCharging = charging
			lastKnownMu.Unlock()

			tooltipText := formatTrayTooltip(level, charging, false)
			trayInvoke(func() {
				applyTrayTooltip(tooltipText)
				updateTrayIcon(level, charging, false)
			})

			est := recordBatteryEstimate(level, charging, mouse)
			if est.Valid {
				persistEstimator(est)
			}
			if logger != nil && (est.Samples > 0 || est.Paused) {
				logger.Printf("[ETA] level=%d charging=%v valid=%v paused=%v hours=%.2f conf=%.2f samples=%d", level, charging, est.Valid, est.Paused, est.Hours, est.Confidence, est.Samples)
			}
			etaPayload := buildEtaPayload(est, level, charging)

			payload := map[string]interface{}{
				"status": "connected", "level": level, "charging": charging, "lastKnown": false,
			}
			if etaPayload != nil {
				payload["timeRemaining"] = etaPayload
			}
			broadcast(payload)
			return
		}

		failCount++
		if failCount >= 2 {
			if logger != nil {
				logger.Printf("[DRIVER] Multiple failures, closing and rescanning")
			}
			mouse.Close()
			currentMouse = nil
			device = nil
			failCount = 0
		} else {
			return
		}
	}

	newMouse, err := findAndConnectMouse()
	currentMouse = newMouse
	if newMouse != nil {
		device = newMouse.handle
	} else {
		device = nil
	}

	if err != nil {
		if !linkDown {
			linkDown = true

			if wasCharging && batteryLvl > 0 {
				if batteryLvl >= lastChargeLevel || batteryLvl >= 10 {
					lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
					lastChargeLevel = batteryLvl
					saveChargeData()
					if logger != nil {
						logger.Printf("[CHARGE] saved charge completion on unplug: time=%s level=%d (wasCharging=%v)", lastChargeTime, lastChargeLevel, wasCharging)
					}
				}
			}

			lastKnownMu.Lock()
			lk := lastKnownLevel
			lkchg := lastKnownCharging
			showLastKnown = true
			lastKnownMu.Unlock()

			if lk >= 0 {
				batteryLvl = lk
				isCharging = lkchg
				trayInvoke(func() {
					tooltipText := formatTrayTooltip(lk, lkchg, true)
					applyTrayTooltip(tooltipText)
					updateTrayIcon(lk, lkchg, true)
				})
				broadcast(map[string]interface{}{"status": "disconnected", "level": lk, "charging": lkchg, "lastKnown": true})
			} else {
				batteryLvl = 0
				isCharging = false
				trayInvoke(func() {
					tooltipText := formatTrayTooltip(-1, false, false)
					applyTrayTooltip(tooltipText)
					updateTrayIcon(0, false, false)
				})
				broadcast(map[string]interface{}{"status": "disconnected", "level": 0, "charging": false, "lastKnown": false})
			}
		}
		return
	} else {
		linkDown = false
		level, charging, _ := newMouse.ReadBattery()
		batteryLvl = level
		isCharging = charging
		deviceModelName = newMouse.modelName
		currentHIDPath = newMouse.path

		lastKnownMu.Lock()
		showLastKnown = false
		lastKnownLevel = batteryLvl
		lastKnownCharging = isCharging
		lastKnownMu.Unlock()

		trayInvoke(func() {
			tooltipText := formatTrayTooltip(batteryLvl, isCharging, false)
			applyTrayTooltip(tooltipText)
			updateTrayIcon(batteryLvl, isCharging, false)
		})
		broadcast(map[string]interface{}{"status": "connected", "level": batteryLvl, "charging": isCharging, "lastKnown": false})
	}
}

func readBattery() (int, bool) {
	mouseMutex.Lock()
	mouse := currentMouse
	mouseMutex.Unlock()
	if mouse == nil {
		return -1, false
	}
	level, charging, err := mouse.ReadBattery()
	if err != nil {
		return -1, false
	}
	return level, charging
}

func safeCloseDevice() {
	mouseMutex.Lock()
	defer mouseMutex.Unlock()
	if currentMouse != nil {
		currentMouse.Close()
		currentMouse = nil
	}
	device = nil
}

func isGloriousVendor(vid uint16) bool { _, ok := knownDevices[vid]; return ok }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isVendorAllowed(vid uint16) bool {
	return allowedVendorIDs[vid]
}

func isTelemetryInterface(info *hid.DeviceInfo) bool {
	usagePage := info.UsagePage
	if usagePage == 0x0001 {
		return info.Usage == 0x02
	}
	if usagePage >= 0xFF00 && usagePage <= 0xFFFF {
		return true
	}
	return false
}

func isDeviceWhitelisted(info *hid.DeviceInfo) bool {
	if info == nil {
		return false
	}
	if !isVendorAllowed(info.VendorID) {
		return false
	}
	if !isTelemetryInterface(info) {
		return false
	}
	if _, ok := knownDevices[info.VendorID]; ok {
		if _, pidOk := knownDevices[info.VendorID][info.ProductID]; pidOk {
			return true
		}
	}
	return false
}

func shouldSkipDeviceInSafeMode(info *hid.DeviceInfo) bool {
	if info == nil {
		return true
	}
	if isKeyboardInterface(info) {
		return true
	}
	if !isDeviceWhitelisted(info) {
		return true
	}
	return false
}

func canWriteToDevice(info *hid.DeviceInfo) bool {
	if isDeviceWhitelisted(info) {
		return true
	}
	return !settings.SafeMode
}

func logDeviceOpen(path string, info *hid.DeviceInfo, readOnly bool) {
	if logger == nil {
		return
	}
	mode := "READ-WRITE"
	if readOnly {
		mode = "READ-ONLY"
	}
	logger.Printf("[HID] Opening device: %s | VID:0x%04X PID:0x%04X | Mode:%s | UsagePage:0x%04X Usage:0x%04X",
		path, info.VendorID, info.ProductID, mode, info.UsagePage, info.Usage)
}

func logBlockedWrite(path string, reason string) {
	if logger == nil {
		return
	}
	logger.Printf("[SAFE_MODE] BLOCKED write to %s: %s", path, reason)
}

// Stubs
func tryImmediateWorkerQuickProbe() bool { return false }
func findDeviceInfoByPath(path string) *hid.DeviceInfo {
	if path == "" {
		return nil
	}
	var found *hid.DeviceInfo
	hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
		if info.Path == path {
			copy := *info
			found = &copy
			return fmt.Errorf("found")
		}
		return nil
	})
	return found
}
func scheduleForceCloseIfStale(path string)                         {}
func clearBackoffsForCandidates()                                   {}
func getProbeWorker() *WorkerClient                                 { return nil }
func qualifiesForWorkerManaged(info *hid.DeviceInfo) bool           { return false }
func clearWriteFailures(path ...string)                             {}
func cancelDelayedReadOnlyLocalFallback(path ...string)             {}
func cancelScheduledForceClose(path ...string)                      {}
func adoptWorkerManagedPath(info *hid.DeviceInfo) (int, bool, bool) { return -1, false, false }
func setWorkerManagedDevice(v bool)                                 { workerManagedDevice = v }
func isWorkerManagedDevice() bool                                   { return workerManagedDevice }
func attachWorkerFramesToInput(ch chan []byte)                      {}
func safeCloseByteChan(ch chan<- []byte, name string)               {}
func handleWorkerSessionError(err error, path string)               {}
func goSafe(name string, f func())                                  { go f() }
func safeOpenPath(path string) (*hid.Device, error) {
	info := findDeviceInfoByPath(path)
	readOnly := settings.SafeMode
	if info != nil {
		logDeviceOpen(path, info, readOnly)
		if settings.SafeMode && !isDeviceWhitelisted(info) {
			if logger != nil {
				logger.Printf("[SAFE_MODE] Blocking open for non-whitelisted device: %s (VID:0x%04X PID:0x%04X)", path, info.VendorID, info.ProductID)
			}
			return nil, fmt.Errorf("safe mode blocked device")
		}
	} else {
		if settings.SafeMode {
			if logger != nil {
				logger.Printf("[SAFE_MODE] Blocking open for unknown device without metadata: %s", path)
			}
			return nil, fmt.Errorf("safe mode blocked unknown device")
		}
		if logger != nil {
			logger.Printf("[HID] Opening device without metadata: %s", path)
		}
	}
	dev, err := hid.OpenPath(path)
	if err != nil {
		return nil, err
	}
	return dev, nil
}
func safeDeviceRead(d *hid.Device, buf []byte) (int, error) {
	return d.Read(buf)
}
func safeGetFeatureReport(d *hid.Device, buf []byte) (int, error) {
	return d.GetFeatureReport(buf)
}
func quickValidate(d *hid.Device) (int, bool, bool)                { return -1, false, false }
func likelyNoMouse(data []byte) bool                               { return false }
func tryProbeDevice(d *hid.Device) (int, bool, bool, byte)         { return -1, false, false, 0x00 }
func allProbeRIDsIncorrect(d *hid.Device) bool                     { return false }
func sendBatteryCommandWithReportID(d *hid.Device, rid byte) error { return nil }
func sendBatteryCommand(d *hid.Device) error                       { return nil }
func getBatteryFromInputReports(d *hid.Device, rid byte, quick bool) (int, bool, bool) {
	return -1, false, false
}
func getBatteryFromInputReportsQuick(d *hid.Device, rid byte) (int, bool, bool) {
	return -1, false, false
}
func sendBatteryFeatureAnyLen(d *hid.Device, rid byte, data []byte) error       { return nil }
func getBatteryFeatureAnyLen(d *hid.Device, rid byte, rlen int) ([]byte, error) { return nil, nil }
func ensureInputReader(d *hid.Device)                                           {}
func isKeyboardInterface(info *hid.DeviceInfo) bool {
	return info.UsagePage == 0x0001 && info.Usage == 0x06
}
func shouldSkipCandidate(info *hid.DeviceInfo) bool    { return false }
func testBattery()                                     {}
func parseBatteryReport(data []byte) (int, bool, bool) { return parseBattery(data) }

var (
	cachedProfile       *DeviceProfile
	workerManagedDevice bool
	deviceMu            sync.Mutex
	gloriousVendorIDs   = []uint16{0x258a, 0x093A}
	deviceNames         = map[uint16]string{}
	allowedVendorIDs    = map[uint16]bool{0x258a: true, 0x093A: true}
)

type DeviceProfile struct {
	Path            string `json:"path"`
	VendorID        uint16 `json:"vendorId"`
	ProductID       uint16 `json:"productId"`
	InterfaceNbr    int    `json:"interfaceNbr,omitempty"`
	Mode            string `json:"mode,omitempty"`
	ReportID        byte   `json:"reportId"`
	ReportLen       int    `json:"reportLen"`
	UseGetOnly      bool   `json:"useGetOnly"`
	UseInputReports bool   `json:"useInputReports"`
}

func init() {
	for _, devices := range knownDevices {
		for pid, name := range devices {
			deviceNames[pid] = name
		}
	}
	initDriver()
}
