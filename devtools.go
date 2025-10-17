package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	rawReportMu   sync.Mutex
	lastRawReport []byte
)

func setLastRawReport(buf []byte) {
	if len(buf) == 0 {
		return
	}
	dup := make([]byte, len(buf))
	copy(dup, buf)
	rawReportMu.Lock()
	lastRawReport = dup
	rawReportMu.Unlock()
}

func getLastRawReport() ([]byte, bool) {
	rawReportMu.Lock()
	defer rawReportMu.Unlock()
	if len(lastRawReport) == 0 {
		return nil, false
	}
	dup := make([]byte, len(lastRawReport))
	copy(dup, lastRawReport)
	return dup, true
}

func waitForInputFrame(timeout time.Duration) ([]byte, error) {
	inputMu.Lock()
	ch := inputFrames
	inputMu.Unlock()
	if ch == nil {
		ensureInputReader(device)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		inputMu.Lock()
		ch = inputFrames
		inputMu.Unlock()
		if ch == nil {
			time.Sleep(40 * time.Millisecond)
			continue
		}
		select {
		case frame, ok := <-ch:
			if !ok {
				return nil, errors.New("input stream closed")
			}
			if len(frame) == 0 {
				continue
			}
			out := make([]byte, len(frame))
			copy(out, frame)
			return out, nil
		case <-time.After(60 * time.Millisecond):
		}
	}
	return nil, errors.New("timed out waiting for input report")
}

func captureHIDReport() ([]byte, error) {
	if device == nil {
		return nil, errors.New("device not connected")
	}

	if safeForInput && useInputReports {
		if frame, err := waitForInputFrame(500 * time.Millisecond); err == nil {
			setLastRawReport(frame)
			return frame, nil
		}
		if err := sendBatteryCommandWithReportID(device, selectedReportID); err == nil {
			time.Sleep(120 * time.Millisecond)
			if frame, err := waitForInputFrame(700 * time.Millisecond); err == nil {
				setLastRawReport(frame)
				return frame, nil
			}
		}
	}

	if !useGetOnly {
		if err := sendBatteryCommandWithReportID(device, selectedReportID); err == nil {
			time.Sleep(120 * time.Millisecond)
		}
	}

	rl := selectedReportLen
	if rl < 2 || rl > 65 {
		rl = 65
	}

	buf := make([]byte, rl)
	buf[0] = selectedReportID
	if n, err := device.GetFeatureReport(buf); err == nil && n > 0 {
		report := append([]byte(nil), buf[:n]...)
		setLastRawReport(report)
		return report, nil
	}

	big := make([]byte, 65)
	big[0] = selectedReportID
	if n, err := device.GetFeatureReport(big); err == nil && n > 0 {
		report := append([]byte(nil), big[:n]...)
		setLastRawReport(report)
		return report, nil
	}

	return nil, errors.New("device did not return a report")
}

func hexDump(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		sb.WriteString(fmt.Sprintf("%04X: ", i))
		for j := i; j < end; j++ {
			sb.WriteString(fmt.Sprintf("%02X ", data[j]))
		}
		if pad := 16 - (end - i); pad > 0 {
			sb.WriteString(strings.Repeat("   ", pad))
		}
		sb.WriteString(" |")
		for j := i; j < end; j++ {
			b := data[j]
			if b >= 32 && b <= 126 {
				sb.WriteByte(b)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteString("|\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func hexString(report []byte) string {
	if len(report) == 0 {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(report))
}

func saveHIDReport(report []byte) (string, error) {
	if len(report) == 0 {
		return "", errors.New("report is empty")
	}

	dir := dataDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Dir(settingsFile)
	}
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("hid_report_%s.txt", ts)
	full := filepath.Join(dir, filename)

	dump := hexDump(report)
	body := fmt.Sprintf("# HID report captured %s\nlen=%d bytes\n\n%s\n\nraw=%s\n",
		time.Now().Format(time.RFC3339), len(report), dump, hexString(report))

	if err := os.WriteFile(full, []byte(body), 0644); err != nil {
		return "", err
	}
	return full, nil
}
