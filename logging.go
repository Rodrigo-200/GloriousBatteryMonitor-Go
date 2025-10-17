package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sstallion/go-hid"
)

func setupLogging() {
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
	}

	logFileHandle, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		logFileHandle, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Printf("Failed to open log file: %v", err)
			return
		}
	}

	log.SetOutput(logFileHandle)
	log.SetFlags(log.LstdFlags)

	logger = log.New(logFileHandle, "", log.LstdFlags)

	logger.Printf("=== Glorious Battery Monitor v%s Started ===", currentVersion)
	logger.Printf("Log file location: %s", logFile)

	scanAllDevices()
}

func scanAllDevices() {
	if logger == nil {
		return
	}

	logger.Printf("=== Scanning for HID devices ===")
	for _, vid := range gloriousVendorIDs {
		logger.Printf("Scanning for Glorious VID: 0x%04x", vid)
		found := 0
		hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
			if shouldSkipCandidate(info) {
				return nil
			}
			logger.Printf("Glorious-ish match - VID: 0x%04x, PID: 0x%04x, Prod: %q, Path: %s",
				info.VendorID, info.ProductID, info.ProductStr, info.Path)
			found++
			return nil
		})
		if found == 0 {
			logger.Printf("No devices found for VID: 0x%04x", vid)
		}
	}

	logger.Printf("Fallback probe for suspicious-but-possible Glorious interfaces…")
	count := 0
	hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
		if count >= 15 {
			return nil
		}
		if shouldSkipCandidate(info) {
			return nil
		}
		looksGlorious := strings.Contains(strings.ToLower(info.ProductStr), "glorious")
		isVendorPage := info.UsagePage >= 0xFF00
		_, pidKnown := deviceNames[info.ProductID]
		if looksGlorious || isVendorPage || pidKnown {
			logger.Printf("Fallback candidate - VID: 0x%04x, PID: 0x%04x, Prod: %q, UsagePage: 0x%04x, If#: %d, Path: %s",
				info.VendorID, info.ProductID, info.ProductStr, info.UsagePage, info.InterfaceNbr, info.Path)
			count++
		}
		return nil
	})
}

func logConn(path string, mode string) {
	if logger != nil {
		m := "FEATURE"
		if useInputReports {
			m = "INPUT"
		}
		logger.Printf("Connected on %s (RID=0x%02x, mode=%s, len=%d) [%s]", path, selectedReportID, m, selectedReportLen, mode)
	}
}
