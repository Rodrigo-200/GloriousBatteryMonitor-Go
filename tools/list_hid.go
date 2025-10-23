package main

import (
	"fmt"

	"github.com/sstallion/go-hid"
)

func main() {
	hid.Init()
	defer hid.Exit()

	fmt.Println("HID Devices:")
	hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
		fmt.Printf("VID: 0x%04x, PID: 0x%04x, Path: %s, Product: %s, UsagePage: 0x%04x, Usage: 0x%02x, Interface: %d\n",
			info.VendorID, info.ProductID, info.Path, info.ProductStr, info.UsagePage, info.Usage, info.InterfaceNbr)
		return nil
	})
}
