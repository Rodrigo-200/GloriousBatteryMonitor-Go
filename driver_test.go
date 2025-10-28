//go:build windows

package main

import (
    "testing"

    "github.com/sstallion/go-hid"
)

func TestIsVendorAllowed(t *testing.T) {
    tests := []struct {
        name     string
        vid      uint16
        expected bool
    }{
        {"Glorious VID 0x258a", 0x258a, true},
        {"Glorious VID 0x093A", 0x093A, true},
        {"Logitech VID", 0x046d, false},
        {"Generic VID", 0x0001, false},
        {"Unknown VID", 0xFFFF, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := isVendorAllowed(tt.vid)
            if result != tt.expected {
                t.Errorf("isVendorAllowed(0x%04X) = %v, want %v", tt.vid, result, tt.expected)
            }
        })
    }
}

func TestIsTelemetryInterface(t *testing.T) {
    tests := []struct {
        name      string
        usagePage uint16
        usage     uint16
        expected  bool
    }{
        {"Vendor page FF00", 0xFF00, 0x0000, true},
        {"Vendor page FFFF", 0xFFFF, 0x0000, true},
        {"Vendor page FF42", 0xFF42, 0x0001, true},
        {"Generic mouse", 0x0001, 0x02, true},
        {"Keyboard", 0x0001, 0x06, false},
        {"Consumer control", 0x0001, 0x01, false},
        {"System control", 0x0001, 0x04, false},
        {"Joystick", 0x0001, 0x05, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            info := &hid.DeviceInfo{
                UsagePage: tt.usagePage,
                Usage:     tt.usage,
            }
            result := isTelemetryInterface(info)
            if result != tt.expected {
                t.Errorf("isTelemetryInterface(UsagePage:0x%04X Usage:0x%04X) = %v, want %v",
                    tt.usagePage, tt.usage, result, tt.expected)
            }
        })
    }
}

func TestIsDeviceWhitelisted(t *testing.T) {
    // Mock settings for testing
    originalSafeMode := settings.SafeMode
    defer func() { settings.SafeMode = originalSafeMode }()

    tests := []struct {
        name     string
        info     *hid.DeviceInfo
        expected bool
    }{
        {
            name: "Model D Wireless - whitelisted",
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x2023,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: true,
        },
        {
            name: "Model D 2 Wireless - whitelisted",
            info: &hid.DeviceInfo{
                VendorID:  0x093A,
                ProductID: 0x824D,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: true,
        },
        {
            name: "Glorious keyboard - not whitelisted (wrong usage page)",
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x2023,
                UsagePage: 0x0001,
                Usage:     0x06,
            },
            expected: false,
        },
        {
            name: "Non-Glorious device - not allowed",
            info: &hid.DeviceInfo{
                VendorID:  0x046d,
                ProductID: 0xc332,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: false,
        },
        {
            name: "Unknown Glorious PID - not whitelisted",
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x9999,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: false,
        },
        {
            name:     "Nil device info",
            info:     nil,
            expected: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := isDeviceWhitelisted(tt.info)
            if result != tt.expected {
                if tt.info != nil {
                    t.Errorf("isDeviceWhitelisted(VID:0x%04X PID:0x%04X UsagePage:0x%04X) = %v, want %v",
                        tt.info.VendorID, tt.info.ProductID, tt.info.UsagePage, result, tt.expected)
                } else {
                    t.Errorf("isDeviceWhitelisted(nil) = %v, want %v", result, tt.expected)
                }
            }
        })
    }
}

func TestShouldSkipDeviceInSafeMode(t *testing.T) {
    originalSafeMode := settings.SafeMode
    settings.SafeMode = true
    defer func() { settings.SafeMode = originalSafeMode }()

    tests := []struct {
        name     string
        info     *hid.DeviceInfo
        expected bool
    }{
        {
            name: "Known Glorious mouse - should not skip",
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x2023,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: false,
        },
        {
            name: "Keyboard interface - should skip",
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x2023,
                UsagePage: 0x0001,
                Usage:     0x06,
            },
            expected: true,
        },
        {
            name: "Non-Glorious device - should skip",
            info: &hid.DeviceInfo{
                VendorID:  0x046d,
                ProductID: 0xc332,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: true,
        },
        {
            name:     "Nil device - should skip",
            info:     nil,
            expected: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := shouldSkipDeviceInSafeMode(tt.info)
            if result != tt.expected {
                if tt.info != nil {
                    t.Errorf("shouldSkipDeviceInSafeMode(VID:0x%04X PID:0x%04X) = %v, want %v",
                        tt.info.VendorID, tt.info.ProductID, result, tt.expected)
                } else {
                    t.Errorf("shouldSkipDeviceInSafeMode(nil) = %v, want %v", result, tt.expected)
                }
            }
        })
    }
}

func TestCanWriteToDevice(t *testing.T) {
    originalSafeMode := settings.SafeMode
    defer func() { settings.SafeMode = originalSafeMode }()

    tests := []struct {
        name     string
        safeMode bool
        info     *hid.DeviceInfo
        expected bool
    }{
        {
            name:     "SafeMode enabled, whitelisted device - can write",
            safeMode: true,
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x2023,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: true,
        },
        {
            name:     "SafeMode enabled, non-whitelisted device - cannot write",
            safeMode: true,
            info: &hid.DeviceInfo{
                VendorID:  0x046d,
                ProductID: 0xc332,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: false,
        },
        {
            name:     "SafeMode disabled, whitelisted device - can write",
            safeMode: false,
            info: &hid.DeviceInfo{
                VendorID:  0x258a,
                ProductID: 0x2023,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: true,
        },
        {
            name:     "SafeMode disabled, non-whitelisted device - can write",
            safeMode: false,
            info: &hid.DeviceInfo{
                VendorID:  0x046d,
                ProductID: 0xc332,
                UsagePage: 0xFF00,
                Usage:     0x0001,
            },
            expected: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            settings.SafeMode = tt.safeMode
            result := canWriteToDevice(tt.info)
            if result != tt.expected {
                t.Errorf("canWriteToDevice(SafeMode:%v, VID:0x%04X) = %v, want %v",
                    tt.safeMode, tt.info.VendorID, result, tt.expected)
            }
        })
    }
}

func TestIsKeyboardInterface(t *testing.T) {
    tests := []struct {
        name      string
        usagePage uint16
        usage     uint16
        expected  bool
    }{
        {"Keyboard", 0x0001, 0x06, true},
        {"Mouse", 0x0001, 0x02, false},
        {"Vendor", 0xFF00, 0x06, false},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            info := &hid.DeviceInfo{
                UsagePage: tt.usagePage,
                Usage:     tt.usage,
            }
            result := isKeyboardInterface(info)
            if result != tt.expected {
                t.Errorf("isKeyboardInterface(UsagePage:0x%04X Usage:0x%04X) = %v, want %v",
                    tt.usagePage, tt.usage, result, tt.expected)
            }
        })
    }
}
