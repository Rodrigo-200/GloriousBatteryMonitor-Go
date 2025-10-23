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

	// Test all the Glorious interfaces we found
	interfaces := map[string]string{
		"MI_02":       `\\?\HID#VID_258A&PID_2023&MI_02#7&2e06099b&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
		"MI_01_Col01": `\\?\HID#VID_258A&PID_2023&MI_01&Col01#7&59b5bd2&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
		"MI_01_Col02": `\\?\HID#VID_258A&PID_2023&MI_01&Col02#7&59b5bd2&0&0001#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
		"MI_01_Col03": `\\?\HID#VID_258A&PID_2023&MI_01&Col03#7&59b5bd2&0&0002#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
		"MI_01_Col04": `\\?\HID#VID_258A&PID_2023&MI_01&Col04#7&59b5bd2&0&0003#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
		"MI_01_Col05": `\\?\HID#VID_258A&PID_2023&MI_01&Col05#7&59b5bd2&0&0004#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
		"MI_00":       `\\?\HID#VID_258A&PID_2023&MI_00#7&2e06099b&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`,
	}

	for name, path := range interfaces {
		fmt.Printf("\n\n=== Testing %s ===\n", name)
		fmt.Printf("Path: %s\n", path)

		d, err := hid.OpenPath(path)
		if err != nil {
			log.Printf("Failed to open device %s: %v", name, err)
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
			// Try to parse battery data
			if n >= 9 {
				for offset := 0; offset <= 2; offset++ {
					tokIdx := 6 + offset
					chgIdx := 7 + offset
					batIdx := 8 + offset
					if tokIdx < n && chgIdx < n && batIdx < n {
						token := buf[tokIdx]
						if token == 0x83 || token == 0x82 || token == 0x81 || token == 0x80 {
							level := int(buf[batIdx])
							charging := buf[chgIdx] == 1
							fmt.Printf(" -> BATTERY: %d%% charging=%v", level, charging)
							break
						}
					}
				}
			}
			fmt.Println()
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
						tokIdx := 6 + offset
						chgIdx := 7 + offset
						batIdx := 8 + offset
						if tokIdx < n && chgIdx < n && batIdx < n {
							token := buf[tokIdx]
							if token == 0x83 || token == 0x82 || token == 0x81 || token == 0x80 {
								level := int(buf[batIdx])
								charging := buf[chgIdx] == 1
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

		d.Close()
	}
}
