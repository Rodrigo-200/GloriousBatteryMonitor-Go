package main

import (
	"fmt"
	"log"
	"time"

	"github.com/sstallion/go-hid"
)

func testInputReports() {
	hid.Init()
	defer hid.Exit()

	// Test MI_02 interface with input reports
	path := `\\?\HID#VID_258A&PID_2023&MI_02#7&2e06099b&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`

	fmt.Printf("Testing MI_02 with input reports: %s\n", path)

	d, err := hid.OpenPath(path)
	if err != nil {
		log.Fatalf("Failed to open device: %v", err)
	}
	defer d.Close()

	// Try reading input reports
	fmt.Println("\nReading input reports:")
	sizes := []int{65, 33, 16, 9, 128, 256}
	for _, sz := range sizes {
		fmt.Printf("Trying buffer size %d:\n", sz)
		for i := 0; i < 3; i++ {
			buf := make([]byte, sz)
			n, err := d.Read(buf)
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
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Try different commands before reading input reports
	fmt.Println("\nTrying different commands before reading input reports:")

	commands := [][]byte{
		{0x00, 0x00, 0x02, 0x02, 0x00, 0x83}, // Current battery command
		{0x00, 0x00, 0x02, 0x02, 0x00, 0x80}, // Try 0x80
		{0x00, 0x00, 0x02, 0x02, 0x00, 0x81}, // Try 0x81
		{0x00, 0x00, 0x02, 0x02, 0x00, 0x84}, // Try 0x84
		{0x00, 0x00, 0x02, 0x00, 0x00, 0x83}, // Different format
		{0x00, 0x00, 0x01, 0x01, 0x00, 0x83}, // Different format
	}

	for i, cmd := range commands {
		fmt.Printf("Command %d: %x\n", i+1, cmd)
		if _, err := d.SendFeatureReport(cmd); err != nil {
			fmt.Printf("  Failed to send: %v\n", err)
			continue
		}

		// Read a few input reports after command
		for j := 0; j < 3; j++ {
			time.Sleep(100 * time.Millisecond)
			buf := make([]byte, 65)
			n, err := d.Read(buf)
			if err != nil {
				fmt.Printf("    Read %d: ERROR - %v\n", j+1, err)
				continue
			}
			fmt.Printf("    Read %d: Got %d bytes: ", j+1, n)
			for k := 0; k < n && k < 20; k++ {
				fmt.Printf("%02x ", buf[k])
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
	}
}

func main() {
	testInputReports()
}
