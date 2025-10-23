package main

import (
	"fmt"
	"log"

	"github.com/sstallion/go-hid"
)

func main() {
	hid.Init()
	defer hid.Exit()

	// Find the Glorious device MI_02
	var device *hid.Device
	hid.Enumerate(0x258a, 0, func(info *hid.DeviceInfo) error {
		if info.ProductID == 0x2023 && info.UsagePage == 0xffff {
			d, err := hid.OpenPath(info.Path)
			if err == nil {
				device = d
				return nil
			}
		}
		return nil
	})

	if device == nil {
		log.Fatal("Device not found")
	}

	defer device.Close()

	// Send command
	cmd := []byte{0x00, 0x00, 0x00, 0x02, 0x02, 0x00, 0x83}
	buf := make([]byte, 65)
	buf[0] = 0x00
	copy(buf[1:], cmd)
	_, err := device.SendFeatureReport(buf)
	if err != nil {
		log.Fatal(err)
	}

	// Read response
	buf = make([]byte, 65)
	buf[0] = 0x00
	n, err := device.GetFeatureReport(buf)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: ")
	for i := 0; i < n; i++ {
		fmt.Printf("%02x ", buf[i])
	}
	fmt.Println()

	// Possible parsings
	if n > 8 {
		fmt.Printf("buf[1]=%d, scaled %d%%\n", buf[1], int(buf[1])*100/255)
		fmt.Printf("buf[8]=%d\n", buf[8])
		if buf[6] == 0x83 {
			fmt.Printf("C# style: lvl=%d, chg=%v\n", buf[8], buf[7] == 1)
		}
	}
}
