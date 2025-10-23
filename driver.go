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
		0x2023: "Model D Wireless",
		0x2012: "Model D Wired",
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

func isWirelessInterface(path string) bool {
	return strings.Contains(strings.ToLower(path), "mi_02")
}

func findAndConnectMouse() (*MouseDevice, error) {
	var candidates []hid.DeviceInfo
	hid.Enumerate(0x258a, 0, func(info *hid.DeviceInfo) error {
		if info.UsagePage == 0x0001 && info.Usage == 0x06 {
			return nil
		}
		candidates = append(candidates, *info)
		return nil
	})
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no mouse found")
	}

	for _, info := range candidates {
		dev, err := hid.OpenPath(info.Path)
		if err != nil {
			continue
		}

		cmd := make([]byte, 65)
		cmd[3], cmd[4], cmd[6] = 0x02, 0x02, 0x83
		dev.SendFeatureReport(cmd)
		time.Sleep(50 * time.Millisecond)

		buf := make([]byte, 65)
		n, _ := dev.GetFeatureReport(buf)
		if n <= 0 {
			dev.Close()
			continue
		}
		level, charging, valid := parseBattery(buf[:n])
		if valid {
			mouse := &MouseDevice{
				handle:     dev,
				path:       info.Path,
				modelName:  knownDevices[0x258a][info.ProductID],
				isWireless: isWirelessInterface(info.Path),
			}
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

			if charging {
				if lastChargeLevel2 < 0 {
					lastChargeLevel2 = level
					lastChargeTime2 = time.Now()
					if logger != nil {
						logger.Printf("[RATE] Initialized charge tracking: level=%d", level)
					}
				} else if level > lastChargeLevel2 {
					if (level - lastChargeLevel2) >= 1 {
						elapsed := time.Since(lastChargeTime2).Hours()
						if elapsed > 0.01 {
							newRate := float64(level-lastChargeLevel2) / elapsed
							chargeRateHistory = append(chargeRateHistory, newRate)
							if len(chargeRateHistory) > 10 {
								chargeRateHistory = chargeRateHistory[1:]
							}
							chargeRate = calculateEMA(chargeRateHistory)
							if logger != nil {
								logger.Printf("[RATE] Charge rate updated: delta=%d%%, elapsed=%.2fh, newRate=%.2f%%/h, EMA=%.2f%%/h, samples=%d", level-lastChargeLevel2, elapsed, newRate, chargeRate, len(chargeRateHistory))
							}
							lastChargeLevel2 = level
							lastChargeTime2 = time.Now()
							if len(chargeRateHistory) >= 3 {
								saveChargeData()
							}
						}
					}
				}
			} else {
				if lastBatteryLevel < 0 {
					lastBatteryLevel = level
					lastBatteryTime = time.Now()
					if logger != nil {
						logger.Printf("[RATE] Initialized discharge tracking: level=%d", level)
					}
				} else if lastBatteryLevel > level {
					if (lastBatteryLevel - level) >= 1 {
						elapsed := time.Since(lastBatteryTime).Hours()
						if elapsed > 0.01 {
							newRate := float64(lastBatteryLevel-level) / elapsed
							rateHistory = append(rateHistory, newRate)
							if len(rateHistory) > 10 {
								rateHistory = rateHistory[1:]
							}
							dischargeRate = calculateEMA(rateHistory)
							if logger != nil {
								logger.Printf("[RATE] Discharge rate updated: delta=%d%%, elapsed=%.2fh, newRate=%.2f%%/h, EMA=%.2f%%/h, samples=%d", lastBatteryLevel-level, elapsed, newRate, dischargeRate, len(rateHistory))
							}
							lastBatteryLevel = level
							lastBatteryTime = time.Now()
							if len(rateHistory) >= 3 {
								saveChargeData()
							}
						}
					}
				}
			}

			batteryLvl = level
			isCharging = charging
			deviceModel = mouse.modelName
			linkDown = false
			wasCharging = charging

			lastKnownMu.Lock()
			showLastKnown = false
			lastKnownLevel = level
			lastKnownCharging = charging
			lastKnownMu.Unlock()

			trayInvoke(func() {
				updateTrayTooltip(fmt.Sprintf("Battery: %d%%", level))
				updateTrayIcon(level, charging, false)
			})

			timeRemaining := ""
			if !charging && dischargeRate > 0.5 && level > 0 {
				hoursLeft := float64(level) / dischargeRate
				if logger != nil {
					logger.Printf("[TIME] Discharge: level=%d, rate=%.2f, hoursLeft=%.2f", level, dischargeRate, hoursLeft)
				}
				if hoursLeft < 100 && hoursLeft > 0 {
					hours := int(hoursLeft)
					minutes := int((hoursLeft - float64(hours)) * 60)
					if hours >= 24 {
						days := hours / 24
						remainingHours := hours % 24
						if remainingHours > 0 {
							timeRemaining = fmt.Sprintf("%dd %dh", days, remainingHours)
						} else {
							timeRemaining = fmt.Sprintf("%dd", days)
						}
					} else if hours > 0 {
						timeRemaining = fmt.Sprintf("%dh %dm", hours, minutes)
					} else if minutes > 0 {
						timeRemaining = fmt.Sprintf("%dm", minutes)
					}
					if logger != nil {
						logger.Printf("[TIME] Discharge result: timeRemaining='%s'", timeRemaining)
					}
				}
			} else if charging && chargeRate > 0.5 && level < 100 {
				hoursLeft := float64(100-level) / chargeRate
				if logger != nil {
					logger.Printf("[TIME] Charge: level=%d, rate=%.2f, hoursLeft=%.2f", level, chargeRate, hoursLeft)
				}
				if hoursLeft < 100 && hoursLeft > 0 {
					hours := int(hoursLeft)
					minutes := int((hoursLeft - float64(hours)) * 60)
					if hours >= 24 {
						days := hours / 24
						remainingHours := hours % 24
						if remainingHours > 0 {
							timeRemaining = fmt.Sprintf("%dd %dh", days, remainingHours)
						} else {
							timeRemaining = fmt.Sprintf("%dd", days)
						}
					} else if hours > 0 {
						timeRemaining = fmt.Sprintf("%dh %dm", hours, minutes)
					} else if minutes > 0 {
						timeRemaining = fmt.Sprintf("%dm", minutes)
					}
					if logger != nil {
						logger.Printf("[TIME] Charge result: timeRemaining='%s'", timeRemaining)
					}
				}
			} else {
				if logger != nil {
					logger.Printf("[TIME] No calculation: charging=%v, dischargeRate=%.2f, chargeRate=%.2f, level=%d", charging, dischargeRate, chargeRate, level)
				}
			}

			payload := map[string]interface{}{
				"status": "connected", "level": level, "charging": charging, "lastKnown": false,
			}
			if timeRemaining != "" {
				payload["timeRemaining"] = timeRemaining
				if logger != nil {
					logger.Printf("[TIME] Including timeRemaining in broadcast: '%s'", timeRemaining)
				}
			} else {
				if logger != nil {
					logger.Printf("[TIME] NOT including timeRemaining in broadcast (empty string)")
				}
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
					updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
					updateTrayIcon(lk, lkchg, true)
				})
				broadcast(map[string]interface{}{"status": "disconnected", "level": lk, "charging": lkchg, "lastKnown": true})
			} else {
				batteryLvl = 0
				isCharging = false
				trayInvoke(func() {
					updateTrayTooltip("Mouse Not Found")
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
		deviceModel = newMouse.modelName
		currentHIDPath = newMouse.path

		lastKnownMu.Lock()
		showLastKnown = false
		lastKnownLevel = batteryLvl
		lastKnownCharging = isCharging
		lastKnownMu.Unlock()

		trayInvoke(func() {
			updateTrayTooltip(fmt.Sprintf("Battery: %d%%", batteryLvl))
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

// Stubs
func tryImmediateWorkerQuickProbe() bool                            { return false }
func findDeviceInfoByPath(path string) *hid.DeviceInfo              { return nil }
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
func safeOpenPath(path string) (*hid.Device, error)                 { return hid.OpenPath(path) }
func safeDeviceRead(d *hid.Device, buf []byte) (int, error)         { return d.Read(buf) }
func safeGetFeatureReport(d *hid.Device, buf []byte) (int, error)   { return d.GetFeatureReport(buf) }
func quickValidate(d *hid.Device) (int, bool, bool)                 { return -1, false, false }
func likelyNoMouse(data []byte) bool                                { return false }
func tryProbeDevice(d *hid.Device) (int, bool, bool, byte)          { return -1, false, false, 0x00 }
func allProbeRIDsIncorrect(d *hid.Device) bool                      { return false }
func sendBatteryCommandWithReportID(d *hid.Device, rid byte) error  { return nil }
func sendBatteryCommand(d *hid.Device) error                        { return nil }
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
