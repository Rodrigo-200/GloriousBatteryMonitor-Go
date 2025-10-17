package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sstallion/go-hid"
)

var gloriousVendorIDs = []uint16{
	0x258a, // older Glorious
	0x342d, // newer Glorious (keyboards, some mice)
	0x093a, // PixArt (wireless dongles for many Glorious mice)
}

func isGloriousVendor(vid uint16) bool {
	for _, known := range gloriousVendorIDs {
		if vid == known {
			return true
		}
	}
	return false
}

var deviceNames = map[uint16]string{
	0x002f: "Model O Wireless Receiver (Legacy)", // VID 0x258A dongle (older batches)
	0x0036: "Model O Wireless (Legacy)",          // early Model O wireless per vendor USB ID listings
	0x0037: "Model O- Wireless (Legacy)",         // early Model O- wireless per vendor USB ID listings
	0x2011: "Model O", 0x2013: "Model O",
	0x2012: "Model D", 0x2023: "Model D",
	0x2019: "Model O-", 0x2024: "Model O-",
	0x2015: "Model D-", 0x2025: "Model D-",
	0x2036: "Model I", 0x2046: "Model I",
	0x2017: "Model O Pro", 0x2018: "Model O Pro",
	0x2031: "Model D2", 0x2033: "Model D2",
	0x2009: "Model O2", 0x200b: "Model O2",
	0x824d: "Model D2 Wireless (PixArt Dongle)",
	0x2014: "Model I2", 0x2016: "Model I2",
}

// skippedPaths is a short-lived cache of HID paths that returned
// "Incorrect function" for GetFeatureReport. We avoid reprobing them
// for a short duration to reduce noisy DeviceIoControl calls and
// reduce the chance of hitting odd driver behavior on unrelated devices.
var skippedPathsMu sync.Mutex
var skippedPaths = map[string]time.Time{}

type DeviceProfile struct {
	Path            string `json:"path"`
	ReportID        byte   `json:"reportId"`
	ReportLen       int    `json:"reportLen"`
	UseGetOnly      bool   `json:"useGetOnly"`
	UseInputReports bool   `json:"useInputReports"`
}

func ensureInputReader(d *hid.Device) {
	if !safeForInput || d == nil {
		return
	}

	inputMu.Lock()
	if inputFrames != nil && inputDev == d {
		inputMu.Unlock()
		return
	}

	ch := make(chan []byte, 16)
	done := make(chan struct{})
	inputFrames = ch
	inputDev = d
	readerDone = done
	inputMu.Unlock()

	go func(dev *hid.Device, out chan []byte) {
		defer func() {
			if r := recover(); r != nil {
				if logger != nil {
					logger.Printf("input reader recovered: %v", r)
				}
			}
			close(out)

			inputMu.Lock()
			if inputFrames == out {
				inputFrames = nil
				inputDev = nil
			}
			if readerDone != nil {
				close(done)
				if readerDone == done {
					readerDone = nil
				}
			}
			inputMu.Unlock()
		}()

		buf := make([]byte, 65)
		for {
			n, err := dev.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				frame := make([]byte, n)
				copy(frame, buf[:n])
				select {
				case out <- frame:
				default:
				}
			} else {
				time.Sleep(30 * time.Millisecond)
			}
		}
	}(d, ch)
}

func isKeyboardInterface(info *hid.DeviceInfo) bool {
	if info == nil {
		return false
	}

	if info.UsagePage == 0x01 && (info.Usage == 0x06 || info.Usage == 0x07) {
		return true
	}

	if strings.Contains(strings.ToLower(info.Path), `\\kbd`) {
		return true
	}

	lp := strings.ToLower(info.ProductStr)
	return strings.Contains(lp, "keyboard")
}

func shouldSkipCandidate(info *hid.DeviceInfo) bool {
	if info == nil {
		return true
	}

	lp := strings.ToLower(info.ProductStr)
	if info.UsagePage == 0x01 && info.Usage == 0x02 {
		return true
	}
	if isKeyboardInterface(info) {
		if logger != nil {
			logger.Printf("[FILTER] skip %s due to keyboard usage (usagePage=0x%04x usage=0x%02x)", info.Path, info.UsagePage, info.Usage)
		}
		return true
	}
	if strings.Contains(lp, "gmmk") ||
		strings.Contains(lp, "headset") ||
		strings.Contains(lp, "audio") {
		return true
	}

	if strings.Contains(strings.ToLower(info.Path), `\\kbd`) {
		return true
	}

	return false
}

func parseBattery(buf []byte) (int, bool, bool) {
	if len(buf) < 9 {
		return 0, false, false
	}

	isTok := func(b byte) bool { return b == 0x83 || b == 0x82 || b == 0x81 || b == 0x80 }

	for _, off := range []int{0, 1, 2} {
		iTok := 6 + off
		ichg := 7 + off
		ibat := 8 + off
		if iTok < len(buf) && ichg < len(buf) && ibat < len(buf) && isTok(buf[iTok]) {
			lvl := int(buf[ibat])
			chg := buf[ichg] == 1
			if lvl >= 0 && lvl <= 100 {
				return lvl, chg, true
			}
		}
	}

	maxScan := len(buf)
	if maxScan > 20 {
		maxScan = 20
	}
	for i := 0; i < maxScan; i++ {
		if !isTok(buf[i]) {
			continue
		}
		if i+2 < len(buf) {
			ch := buf[i+1]
			lv := int(buf[i+2])
			if (ch == 0 || ch == 1) && lv >= 0 && lv <= 100 {
				return lv, ch == 1, true
			}
		}
		if i+2 < len(buf) {
			lv := int(buf[i+1])
			ch := buf[i+2]
			if (ch == 0 || ch == 1) && lv >= 0 && lv <= 100 {
				return lv, ch == 1, true
			}
		}
	}

	return 0, false, false
}

func sendBatteryFeatureAnyLen(d *hid.Device, reportID byte, body []byte) error {
	sizes := []int{9, 16, 33, 65}
	var lastErr error
	for _, sz := range sizes {
		if sz < 1 {
			continue
		}
		buf := make([]byte, sz)
		buf[0] = reportID
		copy(buf[1:], body)
		if _, err := d.SendFeatureReport(buf); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func getBatteryFeatureAnyLen(d *hid.Device, reportID byte) (int, bool, bool, int) {
	sizes := []int{65, 33, 16, 9}
	for _, sz := range sizes {
		buf := make([]byte, sz)
		buf[0] = reportID
		n, err := d.GetFeatureReport(buf)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "incorrect function") {
				if logger != nil {
					logger.Printf("GetFeatureReport invalid on this path (rid=0x%02x, len=%d): %v", reportID, sz, err)
				}
				return 0, false, false, 0
			}
			if logger != nil {
				logger.Printf("GetFeatureReport err (rid=0x%02x, len=%d): %v", reportID, sz, err)
			}
			continue
		}
		if n > 0 {
			if lvl, chg, ok := parseBattery(buf[:n]); ok {
				return lvl, chg, true, sz
			}
			return 0, false, false, 0
		}
	}
	return 0, false, false, 0
}

func getBatteryFromInputReportsQuick(d *hid.Device) (int, bool, bool) {
	ensureInputReader(d)
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if inputFrames == nil {
			return 0, false, false
		}
		select {
		case frame, ok := <-inputFrames:
			if !ok {
				inputMu.Lock()
				inputFrames = nil
				inputDev = nil
				inputMu.Unlock()
				return 0, false, false
			}
			if len(frame) > 0 {
				if lvl, chg, ok := parseBattery(frame); ok {
					return lvl, chg, true
				}
				if likelyNoMouse(frame) {
					return 0, false, false
				}
			}
		case <-time.After(40 * time.Millisecond):
		}
	}
	return 0, false, false
}

func getBatteryFromInputReports(d *hid.Device, reportID byte, tryPoke bool) (int, bool, bool) {
	ensureInputReader(d)

	if !safeForInput && tryPoke {
		tryPoke = false
	}

	if tryPoke {
		bodies := [][]byte{
			{0x00, 0x02, 0x02, 0x00, 0x83},
			{0x00, 0x02, 0x02, 0x00, 0x80},
			{0x00, 0x02, 0x02, 0x00, 0x81},
			{0x00, 0x02, 0x02, 0x00, 0x84},
		}
		for _, body := range bodies {
			buf := make([]byte, 1+len(body))
			buf[0] = reportID
			copy(buf[1:], body)
			_, _ = d.Write(buf)
			time.Sleep(40 * time.Millisecond)
		}
	}

	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if inputFrames == nil {
			return 0, false, false
		}
		select {
		case frame, ok := <-inputFrames:
			if !ok {
				inputMu.Lock()
				inputFrames = nil
				inputDev = nil
				inputMu.Unlock()
				return 0, false, false
			}
			if len(frame) > 0 {
				if lvl, chg, ok := parseBattery(frame); ok {
					return lvl, chg, true
				}
				if likelyNoMouse(frame) {
					return 0, false, false
				}
			}
		case <-time.After(60 * time.Millisecond):
		}
	}
	return 0, false, false
}

func sendBatteryCommandWithReportID(d *hid.Device, reportID byte) error {
	bodies := [][]byte{
		{0x00, 0x02, 0x02, 0x00, 0x83},
		{0x00, 0x02, 0x02, 0x00, 0x80},
		{0x00, 0x02, 0x02, 0x00, 0x81},
		{0x00, 0x02, 0x02, 0x00, 0x84},
	}
	var lastErr error
	for _, body := range bodies {
		if err := sendBatteryFeatureAnyLen(d, reportID, body); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func sendBatteryCommand(d *hid.Device) error {
	for _, rid := range probeRIDs {
		if err := sendBatteryCommandWithReportID(d, rid); err == nil {
			return nil
		}
	}
	return fmt.Errorf("SendFeatureReport failed for all report IDs")
}

func tryProbeDevice(d *hid.Device) (int, bool, bool, byte) {
	for _, rid := range probeRIDs {
		if safeForInput {
			if lvl, chg, ok := getBatteryFromInputReportsQuick(d); ok {
				selectedReportID = rid
				selectedReportLen = 65
				useGetOnly = true
				useInputReports = true

				if logger != nil {
					logger.Printf("[PROBE] mode=%s rid=0x%02x",
						map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
				}
				return lvl, chg, true, rid
			}
			if lvl, chg, ok := getBatteryFromInputReports(d, rid, true); ok {
				if logger != nil {
					logger.Printf("Probe OK via INPUT+OUTPUT poke (rid=0x%02x)", rid)
				}
				selectedReportID = rid
				selectedReportLen = 65
				useGetOnly = true
				useInputReports = true

				if logger != nil {
					logger.Printf("[PROBE] mode=%s rid=0x%02x",
						map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
				}
				return lvl, chg, true, rid
			}
		}

		if lvl, chg, ok, usedLen := getBatteryFeatureAnyLen(d, rid); ok {
			selectedReportID = rid
			selectedReportLen = usedLen
			useGetOnly = true
			useInputReports = false

			if logger != nil {
				logger.Printf("[PROBE] mode=%s rid=0x%02x",
					map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
			}
			return lvl, chg, true, rid
		}

		if safeForInput {
			if err := sendBatteryCommandWithReportID(d, rid); err == nil {
				time.Sleep(120 * time.Millisecond)
				if lvl, chg, ok, usedLen := getBatteryFeatureAnyLen(d, rid); ok {
					selectedReportID = rid
					selectedReportLen = usedLen
					useGetOnly = false
					useInputReports = false

					if logger != nil {
						logger.Printf("[PROBE] mode=%s rid=0x%02x",
							map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
					}

					return lvl, chg, true, rid
				}
			}
		}
	}
	return 0, false, false, 0
}

func reconnect() {
	if device != nil {
		return
	}

	var candidates []hid.DeviceInfo
	seen := make(map[string]bool)
	for _, vid := range gloriousVendorIDs {
		hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
			// Skip short-term blacklisted paths
			skippedPathsMu.Lock()
			if until, ok := skippedPaths[info.Path]; ok {
				if time.Now().Before(until) {
					skippedPathsMu.Unlock()
					if logger != nil {
						logger.Printf("[FILTER] skip %s due to recent invalid feature report (until %v)", info.Path, until)
					}
					return nil
				}
				delete(skippedPaths, info.Path)
			}
			skippedPathsMu.Unlock()
			if shouldSkipCandidate(info) {
				return nil
			}
			if !seen[info.Path] {
				candidates = append(candidates, *info)
				seen[info.Path] = true
			}
			return nil
		})
	}

	if len(candidates) == 0 {
		hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
			// Skip short-term blacklisted paths
			skippedPathsMu.Lock()
			if until, ok := skippedPaths[info.Path]; ok {
				if time.Now().Before(until) {
					skippedPathsMu.Unlock()
					if logger != nil {
						logger.Printf("[FILTER] skip %s due to recent invalid feature report (until %v)", info.Path, until)
					}
					return nil
				}
				delete(skippedPaths, info.Path)
			}
			skippedPathsMu.Unlock()
			if seen[info.Path] {
				return nil
			}

			if shouldSkipCandidate(info) {
				return nil
			}

			lowProd := strings.ToLower(info.ProductStr)
			looksGlorious := strings.Contains(lowProd, "glorious") ||
				strings.Contains(lowProd, "model o") ||
				strings.Contains(lowProd, "model d") ||
				strings.Contains(lowProd, "model i")
			isVendorPage := info.UsagePage >= 0xFF00
			_, pidKnown := deviceNames[info.ProductID]
			vendorMatches := isGloriousVendor(info.VendorID)
			if looksGlorious || pidKnown || (isVendorPage && vendorMatches) {
				candidates = append(candidates, *info)
				seen[info.Path] = true
			}
			return nil
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		aVnd := (a.UsagePage >= 0xFF00)
		bVnd := (b.UsagePage >= 0xFF00)
		if aVnd != bVnd {
			return aVnd && !bVnd
		}

		if (a.UsagePage >= 0xFF00) != (b.UsagePage >= 0xFF00) {
			return a.UsagePage >= 0xFF00
		}
		if (a.Usage != 0) != (b.Usage != 0) {
			return a.Usage != 0
		}
		if a.InterfaceNbr != b.InterfaceNbr {
			return a.InterfaceNbr < b.InterfaceNbr
		}
		return false
	})

	if cachedProfile != nil {
		for i := range candidates {
			if candidates[i].Path == cachedProfile.Path {
				if i != 0 {
					c := candidates[i]
					copy(candidates[1:i+1], candidates[0:i])
					candidates[0] = c
				}
				break
			}
		}
	}

	for _, ci := range candidates {
		d, err := hid.OpenPath(ci.Path)
		if err != nil {
			continue
		}

		// Quick check: if every probe RID returns "Incorrect function",
		// mark this path as temporarily bad and skip it for a while.
		if allProbeRIDsIncorrect(d) {
			if logger != nil {
				logger.Printf("[RECONNECT] path=%s appears to not support feature reports — skipping for 90s", ci.Path)
			}
			skippedPathsMu.Lock()
			skippedPaths[ci.Path] = time.Now().Add(90 * time.Second)
			skippedPathsMu.Unlock()
			d.Close()
			continue
		}

		prevSafe := safeForInput
		safeForInput = (ci.UsagePage >= 0xFF00)

		if logger != nil {
			logger.Printf("[RECONNECT] trying path=%s usagePage=0x%04x iface=%d", ci.Path, ci.UsagePage, ci.InterfaceNbr)
		}

		if cachedProfile != nil && ci.Path == cachedProfile.Path {
			selectedReportID = cachedProfile.ReportID
			selectedReportLen = cachedProfile.ReportLen
			useGetOnly = cachedProfile.UseGetOnly
			useInputReports = cachedProfile.UseInputReports

			if lvl, chg, ok := quickValidate(d); ok {
				device = d
				deviceModel = pickModel(ci)
				currentHIDPath = ci.Path
				safeForInput = (ci.UsagePage >= 0xFF00)
				logConn(ci.Path, "CACHED")
				finishConnect(ci.Path, lvl, chg)
				return
			}
		}

		lvl, chg, ok, rid := tryProbeDevice(d)
		if ok {
			device = d
			selectedReportID = rid
			deviceModel = pickModel(ci)
			currentHIDPath = ci.Path
			safeForInput = (ci.UsagePage >= 0xFF00)
			logConn(ci.Path, "PROBED")
			saveConnProfile(DeviceProfile{
				Path:            ci.Path,
				ReportID:        selectedReportID,
				ReportLen:       selectedReportLen,
				UseGetOnly:      useGetOnly,
				UseInputReports: useInputReports,
			})

			batteryLvl = lvl
			isCharging = chg
			status := "Discharging"
			icon := "🔋"
			if chg {
				status = "Charging"
				icon = "⚡"
			}
			batteryText = fmt.Sprintf("%s %d%% (%s)", icon, lvl, status)
			updateTrayTooltip(fmt.Sprintf("Battery: %d%%", lvl))
			updateTrayIcon(lvl, chg)
			return
		}
		currentHIDPath = ci.Path
		safeForInput = prevSafe
		d.Close()
	}
}

func quickValidate(d *hid.Device) (int, bool, bool) {
	if useInputReports && safeForInput {
		ensureInputReader(d)
		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			if inputFrames == nil {
				break
			}
			select {
			case frame, ok := <-inputFrames:
				if !ok {
					inputMu.Lock()
					inputFrames = nil
					inputDev = nil
					inputMu.Unlock()
					return 0, false, false
				}
				if len(frame) > 0 {
					if lvl, chg, ok := parseBattery(frame); ok {
						return lvl, chg, true
					}
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
		return 0, false, false
	}

	buf := make([]byte, 65)
	buf[0] = selectedReportID
	if n, err := d.GetFeatureReport(buf); err == nil && n > 0 {
		if lvl, chg, ok := parseBattery(buf[:n]); ok {
			return lvl, chg, true
		}
	}
	return 0, false, false
}

func likelyNoMouse(buf []byte) bool {
	if len(buf) <= 5 {
		return true
	}

	max := len(buf)
	if max > 16 {
		max = 16
	}
	nonTrivial := 0
	for i := 0; i < max; i++ {
		if buf[i] != 0x00 && buf[i] != 0xFF {
			nonTrivial++
		}
	}
	if nonTrivial == 0 {
		return true
	}

	if len(buf) >= 9 {
		zeros := 0
		for i := 1; i < 9; i++ {
			if buf[i] == 0x00 {
				zeros++
			}
		}
		if zeros >= 7 {
			return true
		}
	}
	return false
}

func testBattery(d *hid.Device) (int, bool) {
	lvl, chg, ok, _ := tryProbeDevice(d)
	if ok {
		return lvl, chg
	}
	return 0, false
}

// allProbeRIDsIncorrect returns true if a quick test of each probe RID
// produced only "Incorrect function" errors, suggesting this interface
// does not support feature reports.
func allProbeRIDsIncorrect(d *hid.Device) bool {
	if d == nil {
		return false
	}
	incorrect := 0
	for _, rid := range probeRIDs {
		buf := make([]byte, 9)
		buf[0] = rid
		if _, err := d.GetFeatureReport(buf); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "incorrect function") {
				incorrect++
				continue
			}
			// Some other error; treat as not-conclusive
			return false
		} else {
			// got something; this interface supports feature reports
			return false
		}
	}
	return incorrect == len(probeRIDs)
}

func readBattery() (int, bool) {
	if device == nil {
		linkDown = true
		if logger != nil {
			logger.Printf("[LINK] down on path=%s", currentHIDPath)
		}
		return -1, false
	}

	if useInputReports && safeForInput {
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, false); ok {
			linkDown = false
			recordedUnplug = false

			if logger != nil {
				logger.Printf("[READ] linkDown=%v lvl=%d chg=%v (path=%s, rid=0x%02x, mode=%s len=%d)",
					linkDown, lvl, chg, currentHIDPath, selectedReportID,
					map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], selectedReportLen)
			}

			return lvl, chg
		}
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, true); ok {
			linkDown = false
			recordedUnplug = false

			if logger != nil {
				logger.Printf("[READ] linkDown=%v lvl=%d chg=%v (path=%s, rid=0x%02x, mode=%s len=%d)",
					linkDown, lvl, chg, currentHIDPath, selectedReportID,
					map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], selectedReportLen)
			}

			return lvl, chg
		}
		linkDown = true
		if logger != nil {
			logger.Printf("[LINK] down on path=%s", currentHIDPath)
		}
		return -1, false
	}

	if !useGetOnly {
		if err := sendBatteryCommandWithReportID(device, selectedReportID); err != nil {
			if logger != nil {
				logger.Printf("SendFeatureReport(read) failed (rid=0x%02x): %v (switching to GET-only for this session)", selectedReportID, err)
			}
			useGetOnly = true
		} else {
			time.Sleep(150 * time.Millisecond)
		}
	}

	rl := selectedReportLen
	if rl < 2 || rl > 65 {
		rl = 65
	}

	buf := make([]byte, rl)
	buf[0] = selectedReportID
	n, err := device.GetFeatureReport(buf)
	if err == nil && n > 0 {
		if lvl, chg, ok := parseBattery(buf[:n]); ok {
			linkDown = false
			recordedUnplug = false
			return lvl, chg
		}
		if likelyNoMouse(buf[:n]) {
			linkDown = true
			if logger != nil {
				logger.Printf("[LINK] down on path=%s", currentHIDPath)
			}
			return -1, false
		}
	}

	if rl != 65 {
		big := make([]byte, 65)
		big[0] = selectedReportID
		if n2, err2 := device.GetFeatureReport(big); err2 == nil && n2 > 0 {
			if lvl, chg, ok := parseBattery(big[:n2]); ok {
				selectedReportLen = 65
				linkDown = false
				recordedUnplug = false
				return lvl, chg
			}
			if likelyNoMouse(big[:n2]) {
				linkDown = true
				if logger != nil {
					logger.Printf("[LINK] down on path=%s", currentHIDPath)
				}
				return -1, false
			}
		}
	}

	if safeForInput {
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, false); ok {
			useInputReports = true
			linkDown = false
			recordedUnplug = false
			p := currentHIDPath
			if p == "" && cachedProfile != nil {
				p = cachedProfile.Path
			}
			saveConnProfile(DeviceProfile{
				Path:            p,
				ReportID:        selectedReportID,
				ReportLen:       selectedReportLen,
				UseGetOnly:      true,
				UseInputReports: true,
			})
			return lvl, chg
		}
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, true); ok {
			useInputReports = true
			linkDown = false
			recordedUnplug = false
			return lvl, chg
		}
	}

	linkDown = true
	if logger != nil {
		logger.Printf("[LINK] down on path=%s", currentHIDPath)
	}
	return -1, false
}
