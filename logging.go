//go:build windows
// +build windows

package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"
    "strings"

    "github.com/sstallion/go-hid"
)

func setupLogging() {
    _ = os.MkdirAll(filepath.Dir(logFile), 0755)
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

type HIDDeviceInfo struct {
    Path         string `json:"path"`
    VendorID     string `json:"vendorId"`
    ProductID    string `json:"productId"`
    ProductStr   string `json:"productStr"`
    Manufacturer string `json:"manufacturer"`
    SerialNumber string `json:"serialNumber"`
    UsagePage    string `json:"usagePage"`
    Usage        string `json:"usage"`
    InterfaceNbr int    `json:"interfaceNbr"`
    ReleaseNbr   int    `json:"releaseNbr"`
    IsGlorious   bool   `json:"isGlorious"`
    ModelName    string `json:"modelName"`
}

type HIDScanResult struct {
    GloriousDevices []HIDDeviceInfo `json:"gloriousDevices"`
    AllDevices      []HIDDeviceInfo `json:"allDevices"`
    TotalCount      int             `json:"totalCount"`
    GloriousCount   int             `json:"gloriousCount"`
}

func scanAllHIDDevices() *HIDScanResult {
    result := &HIDScanResult{
        GloriousDevices: []HIDDeviceInfo{},
        AllDevices:      []HIDDeviceInfo{},
    }
    if logger != nil {
        logger.Printf("[HID_SCAN] Starting device enumeration (SafeMode:%v)", settings.SafeMode)
    }
    hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
        devInfo := HIDDeviceInfo{
            Path:         info.Path,
            VendorID:     fmt.Sprintf("0x%04X", info.VendorID),
            ProductID:    fmt.Sprintf("0x%04X", info.ProductID),
            ProductStr:   info.ProductStr,
            Manufacturer: info.MfrStr,
            SerialNumber: info.SerialNbr,
            UsagePage:    fmt.Sprintf("0x%04X", info.UsagePage),
            Usage:        fmt.Sprintf("0x%04X", info.Usage),
            InterfaceNbr: info.InterfaceNbr,
            ReleaseNbr:   int(info.ReleaseNbr),
        }
        isGlorious := false
        for _, vid := range gloriousVendorIDs {
            if info.VendorID == vid {
                isGlorious = true
                break
            }
        }
        if !isGlorious && strings.Contains(strings.ToLower(info.ProductStr), "glorious") {
            isGlorious = true
        }
        devInfo.IsGlorious = isGlorious
        if isGlorious {
            if name, ok := deviceNames[info.ProductID]; ok {
                devInfo.ModelName = name
            } else if info.ProductStr != "" {
                devInfo.ModelName = info.ProductStr
            } else {
                devInfo.ModelName = "Unknown Glorious Device"
            }
            result.GloriousDevices = append(result.GloriousDevices, devInfo)
            result.GloriousCount++
            if logger != nil {
                whitelisted := isDeviceWhitelisted(info)
                willOpen := !settings.SafeMode || whitelisted
                logger.Printf("[HID_SCAN] Glorious device: %s | VID:%s PID:%s | Whitelisted:%v | WillOpen:%v",
                    devInfo.ModelName, devInfo.VendorID, devInfo.ProductID, whitelisted, willOpen)
            }
        }
        result.AllDevices = append(result.AllDevices, devInfo)
        result.TotalCount++
        return nil
    })
    return result
}

func logHIDScanResults(result *HIDScanResult) {
    if logger == nil {
        return
    }
    logger.Printf("=== HID Device Scan Results ===")
    logger.Printf("Total devices found: %d", result.TotalCount)
    logger.Printf("Glorious devices found: %d", result.GloriousCount)
    if result.GloriousCount > 0 {
        logger.Printf("\n--- Glorious Devices ---")
        for i, dev := range result.GloriousDevices {
            logger.Printf("[%d] %s", i+1, dev.ModelName)
            logger.Printf("    VID: %s, PID: %s", dev.VendorID, dev.ProductID)
            logger.Printf("    Product: %s", dev.ProductStr)
            logger.Printf("    Manufacturer: %s", dev.Manufacturer)
            logger.Printf("    Serial: %s", dev.SerialNumber)
            logger.Printf("    UsagePage: %s, Usage: %s", dev.UsagePage, dev.Usage)
            logger.Printf("    Interface: %d, Release: %d", dev.InterfaceNbr, dev.ReleaseNbr)
            logger.Printf("    Path: %s", dev.Path)
        }
    }
    logger.Printf("\n--- All HID Devices ---")
    for i, dev := range result.AllDevices {
        logger.Printf("[%d] VID: %s, PID: %s - %s", i+1, dev.VendorID, dev.ProductID, dev.ProductStr)
        if dev.Manufacturer != "" {
            logger.Printf("    Manufacturer: %s", dev.Manufacturer)
        }
        logger.Printf("    UsagePage: %s, Usage: %s, Interface: %d", dev.UsagePage, dev.Usage, dev.InterfaceNbr)
    }
    logger.Printf("=== End HID Scan ===")
}
