package main

import (
	"fmt"
	"log"
	"time"

	"github.com/sstallion/go-hid"
)

func main() {
	hid.Init()
	defer hid.Exit()

	// Enumerate all HID devices
	fmt.Println("Enumerating HID devices:")
	hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
		if info.VendorID == 0x258a || info.VendorID == 0x342d || info.VendorID == 0x093a {
			fmt.Printf("Found Glorious device: VID=0x%04x PID=0x%04x Path=%s\n", info.VendorID, info.ProductID, info.Path)
		}
		return nil
	})

	// Test all Glorious interfaces found
	var candidates []hid.DeviceInfo
	hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
		if info.VendorID == 0x258a || info.VendorID == 0x342d || info.VendorID == 0x093a {
			candidates = append(candidates, *info)
		}
		return nil
	})

	for _, info := range candidates {
		fmt.Printf("\n\n=== Testing %s ===\n", info.Path)
		fmt.Printf("VID=0x%04x PID=0x%04x UsagePage=0x%04x Usage=0x%02x\n", info.VendorID, info.ProductID, info.UsagePage, info.Usage)

		d, err := hid.OpenPath(info.Path)
		if err != nil {
			log.Printf("Failed to open device %s: %v", info.Path, err)
			continue
		}

		// Test reading without any command first
		fmt.Println("Reading without command:")
		for i := 0; i < 2; i++ {
			buf := make([]byte, 65)
			buf[0] = 0x00
			n, err := d.GetFeatureReport(buf)
			if err != nil {
				fmt.Printf("  Read %d: ERROR - %v\n", i+1, err)
				continue
			}
			fmt.Printf("  Read %d: Got %d bytes: ", i+1, n)
			for j := 0; j < n && j < 20; j++ {
				fmt.Printf("%02x ", buf[j])
			}
			fmt.Println()
			// Try to parse battery data
			if n >= 9 {
				for offset := 0; offset <= 2; offset++ {
					tokIdx := 5 + offset
					chgIdx := 6 + offset
					batIdx := 1 + offset
					if tokIdx < n && chgIdx < n && batIdx < n {
						token := buf[tokIdx]
						if token == 0x83 || token == 0x82 || token == 0x81 || token == 0x80 || token == 0x84 {
							level := int(buf[batIdx])
							if level > 100 {
								level = 100
							}
							charging := buf[chgIdx] == 1 || token == 0x84
							fmt.Printf(" -> BATTERY: %d%% charging=%v (offset=%d)\n", level, charging, offset)
							break
						}
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}

		// Try sending battery command and reading
		fmt.Println("After sending battery command (00 02 02 00 83):")
		cmd := []byte{0x00, 0x00, 0x02, 0x02, 0x00, 0x83}
		if _, err := d.SendFeatureReport(cmd); err == nil {
			time.Sleep(150 * time.Millisecond)
			buf := make([]byte, 65)
			buf[0] = 0x00
			n, err := d.GetFeatureReport(buf)
			if err == nil && n > 0 {
				fmt.Printf("  Got %d bytes: ", n)
				for j := 0; j < n && j < 20; j++ {
					fmt.Printf("%02x ", buf[j])
				}
				// Try to parse battery data
				if n >= 9 {
					for offset := 0; offset <= 2; offset++ {
						tokIdx := 5 + offset
						chgIdx := 6 + offset
						batIdx := 1 + offset
						if tokIdx < n && chgIdx < n && batIdx < n {
							token := buf[tokIdx]
							if token == 0x83 || token == 0x82 || token == 0x81 || token == 0x80 || token == 0x84 {
								level := int(buf[batIdx])
								if level > 100 {
									level = 100
								}
								charging := buf[chgIdx] == 1 || token == 0x84
								fmt.Printf(" -> BATTERY: %d%% charging=%v", level, charging)
								break
							}
						}
					}
				}
				fmt.Println()
			}
		} else {
			fmt.Printf("  Failed to send command: %v\n", err)
		}

		// Try different commands
		commands := [][]byte{
			{0x00, 0x00, 0x01, 0x01, 0x00, 0x83},
			{0x00, 0x00, 0x02, 0x02, 0x00, 0x80},
			{0x00, 0x00, 0x02, 0x02, 0x00, 0x81},
			{0x00, 0x00, 0x02, 0x02, 0x00, 0x84},
			{0x00, 0x00, 0x00, 0x00, 0x00, 0x83},
			{0x01, 0x00, 0x02, 0x02, 0x00, 0x83}, // different report ID
			{0x02, 0x00, 0x02, 0x02, 0x00, 0x83},
			{0x03, 0x00, 0x02, 0x02, 0x00, 0x83},
			{0x04, 0x00, 0x02, 0x02, 0x00, 0x83},
		}

		for i, cmd := range commands {
			fmt.Printf("Trying command %d: %x\n", i+1, cmd)
			if _, err := d.SendFeatureReport(cmd); err != nil {
				fmt.Printf("  Failed to send: %v\n", err)
				continue
			}
			time.Sleep(150 * time.Millisecond)
			buf := make([]byte, 65)
			buf[0] = cmd[0] // use same report ID for read
			n, err := d.GetFeatureReport(buf)
			if err != nil {
				fmt.Printf("  Read error: %v\n", err)
				continue
			}
			fmt.Printf("  Got %d bytes: ", n)
			for j := 0; j < n && j < 20; j++ {
				fmt.Printf("%02x ", buf[j])
			}
			// Try to parse battery data
			if n >= 9 {
				for offset := 0; offset <= 2; offset++ {
					tokIdx := 5 + offset
					chgIdx := 6 + offset
					batIdx := 1 + offset
					if tokIdx < n && chgIdx < n && batIdx < n {
						token := buf[tokIdx]
						if token == 0x83 || token == 0x82 || token == 0x81 || token == 0x80 || token == 0x84 {
							level := int(buf[batIdx])
							if level > 100 {
								level = 100
							}
							charging := buf[chgIdx] == 1 || token == 0x84
							fmt.Printf(" -> BATTERY: %d%% charging=%v", level, charging)
							break
						}
					}
				}
			}
			fmt.Println()
		}

		// Test reading input reports
		fmt.Println("Testing input reports:")
		buf := make([]byte, 9)
		ch := make(chan []byte, 1)
		go func() {
			n, err := d.Read(buf)
			if err == nil {
				ch <- buf[:n]
			} else {
				ch <- nil
			}
		}()
		select {
		case result := <-ch:
			if result != nil {
				fmt.Printf("  Input read: Got %d bytes: ", len(result))
				for j := 0; j < len(result) && j < 20; j++ {
					fmt.Printf("%02x ", result[j])
				}
				fmt.Println()
				// Try to parse battery data
				if len(result) >= 9 {
					for offset := 0; offset <= 2; offset++ {
						tokIdx := 5 + offset
						chgIdx := 6 + offset
						batIdx := 1 + offset
						if tokIdx < len(result) && chgIdx < len(result) && batIdx < len(result) {
							token := result[tokIdx]
							if token == 0x83 || token == 0x82 || token == 0x81 || token == 0x80 || token == 0x84 {
								level := int(result[batIdx])
								if level > 100 {
									level = 100
								}
								charging := result[chgIdx] == 1 || token == 0x84
								fmt.Printf(" -> BATTERY: %d%% charging=%v (offset=%d)\n", level, charging, offset)
								break
							}
						}
					}
				}
			} else {
				fmt.Println("  Input read: ERROR")
			}
		case <-time.After(5 * time.Second):
			fmt.Println("  Input read: TIMEOUT (no input reports received)")
		}
		d.Close()
	}
}
