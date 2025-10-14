package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/lxn/win"
	"github.com/sstallion/go-hid"
)

//go:embed ui.html
var content embed.FS

const (
	VendorID    = 0x258a
	WM_USER     = 0x0400
	WM_TRAYICON = WM_USER + 1
	WM_APP_HIDE = WM_USER + 2
	ID_SHOW     = 1001
	ID_QUIT     = 1002
)

// Supported Glorious mice product IDs
var supportedDevices = []uint16{
	0x2011, // Model O Wired
	0x2013, // Model O Wireless (Dongle)
	0x2012, // Model D Wired
	0x2023, // Model D Wireless (Dongle)
	0x2019, // Model O- Wired
	0x2024, // Model O- Wireless (Dongle)
	0x2015, // Model D- Wired
	0x2025, // Model D- Wireless (Dongle)
	0x2036, // Model I Wired
	0x2046, // Model I Wireless (Dongle)
	0x2017, // Model O Pro Wired
	0x2018, // Model O Pro Wireless (Dongle)
	0x2031, // Model D2 Wired
	0x2033, // Model D2 Wireless (Dongle)
	0x2009, // Model O2 Wired
	0x200b, // Model O2 Wireless (Dongle)
	0x2014, // Model I2 Wired
	0x2016, // Model I2 Wireless (Dongle)
}

var deviceNames = map[uint16]string{
	0x2011: "Model O", 0x2013: "Model O",
	0x2012: "Model D", 0x2023: "Model D",
	0x2019: "Model O-", 0x2024: "Model O-",
	0x2015: "Model D-", 0x2025: "Model D-",
	0x2036: "Model I", 0x2046: "Model I",
	0x2017: "Model O Pro", 0x2018: "Model O Pro",
	0x2031: "Model D2", 0x2033: "Model D2",
	0x2009: "Model O2", 0x200b: "Model O2",
	0x2014: "Model I2", 0x2016: "Model I2",
}

type ChargeData struct {
	LastChargeTime  string `json:"lastChargeTime"`
	LastChargeLevel int    `json:"lastChargeLevel"`
}

type Settings struct {
	StartWithWindows bool `json:"startWithWindows"`
	RefreshInterval  int  `json:"refreshInterval"` // in seconds
}

var (
	device          *hid.Device
	deviceModel     string = "Unknown"
	hwnd            win.HWND
	webviewHwnd     win.HWND
	nid             win.NOTIFYICONDATA
	batteryText     string = "Connecting..."
	batteryLvl      int
	isCharging      bool
	wasCharging     bool
	lastChargeTime  string = "Never"
	lastChargeLevel int    = 0
	user32          = syscall.NewLazyDLL("user32.dll")
	appendMenuW     = user32.NewProc("AppendMenuW")
	setWindowLong   = user32.NewProc("SetWindowLongPtrW")
	showWindow      = user32.NewProc("ShowWindow")
	clients         = make(map[chan string]bool)
	w               webview2.WebView
	serverPort      string = "8765"
	dataFile        string
	settingsFile    string
	settings        Settings
)

func main() {
	// Set up data file paths
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = "."
	}
	dataDir := filepath.Join(appData, "GloriousBatteryMonitor")
	os.MkdirAll(dataDir, 0755)
	dataFile = filepath.Join(dataDir, "charge_data.json")
	settingsFile = filepath.Join(dataDir, "settings.json")
	
	// Load saved data
	loadChargeData()
	loadSettings()
	
	// Allow overriding the embedded web server port via PORT env var (useful for debugging or port conflicts)
	if p := os.Getenv("PORT"); p != "" {
		serverPort = p
	}

	go startWebServer()
	go startTray()
	go updateBattery()

	time.Sleep(500 * time.Millisecond)

	w = webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Glorious Mouse Battery Monitor",
			Width:  500,
			Height: 650,
			IconId: 0,
		},
	})
	if w == nil {
		return
	}
	defer w.Destroy()

	webviewHwnd = win.HWND(w.Window())
	
	// Load and set window icon
	hInst := win.GetModuleHandle(nil)
	hIcon := win.LoadIcon(hInst, win.MAKEINTRESOURCE(1))
	if hIcon != 0 {
		win.SendMessage(webviewHwnd, win.WM_SETICON, 0, uintptr(hIcon)) // Small icon
		win.SendMessage(webviewHwnd, win.WM_SETICON, 1, uintptr(hIcon)) // Large icon
	}
	
	oldProc := win.SetWindowLongPtr(webviewHwnd, win.GWLP_WNDPROC, syscall.NewCallback(webviewWndProc))
	win.SetWindowLongPtr(webviewHwnd, win.GWLP_USERDATA, oldProc)

	w.Navigate(fmt.Sprintf("http://localhost:%s", serverPort))
	w.Run()
}

func startWebServer() {
	http.HandleFunc("/", serveHTML)
	http.HandleFunc("/events", handleSSE)
	http.HandleFunc("/api/settings", handleSettings)
	addr := fmt.Sprintf(":%s", serverPort)
	log.Printf("starting web server on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func serveHTML(w http.ResponseWriter, r *http.Request) {
	data, _ := content.ReadFile("ui.html")
	w.Header().Set("Content-Type", "text/html")
	w.Write(data)
}

func handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	messageChan := make(chan string)
	clients[messageChan] = true
	defer func() { delete(clients, messageChan) }()

	for msg := range messageChan {
		fmt.Fprintf(w, "data: %s\n\n", msg)
		w.(http.Flusher).Flush()
	}
}

func broadcast(data map[string]interface{}) {
	jsonData, _ := json.Marshal(data)
	for client := range clients {
		select {
		case client <- string(jsonData):
		default:
		}
	}
}

func startTray() {
	hInst := win.GetModuleHandle(nil)
	className, _ := syscall.UTF16PtrFromString("GloriousTrayClass")

	wc := win.WNDCLASSEX{
		CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     hInst,
		LpszClassName: className,
	}
	win.RegisterClassEx(&wc)

	windowName, _ := syscall.UTF16PtrFromString("Glorious Tray")
	hwnd = win.CreateWindowEx(0, className, windowName, 0, 0, 0, 0, 0, 0, 0, hInst, nil)

	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
	nid.UCallbackMessage = WM_TRAYICON
	// Create battery icon
	nid.HIcon = createBatteryIcon(0, false)

	tip, _ := syscall.UTF16FromString("Glorious Battery")
	copy(nid.SzTip[:], tip)

	win.Shell_NotifyIcon(win.NIM_ADD, &nid)

	var msg win.MSG
	for win.GetMessage(&msg, 0, 0, 0) > 0 {
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
}

func webviewWndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_CLOSE:
		showWindow.Call(uintptr(hwnd), uintptr(win.SW_HIDE))
		return 0
	case win.WM_DESTROY:
		return 0
	}
	oldProc := win.GetWindowLongPtr(hwnd, win.GWLP_USERDATA)
	return win.CallWindowProc(oldProc, hwnd, msg, wParam, lParam)
}

func wndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TRAYICON:
		switch lParam {
		case win.WM_LBUTTONUP:
			showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
			win.SetForegroundWindow(webviewHwnd)
		case win.WM_RBUTTONUP:
			showMenu()
		case win.WM_LBUTTONDBLCLK:
			showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
			win.SetForegroundWindow(webviewHwnd)
		}
		return 0
	case win.WM_COMMAND:
		switch wParam {
		case ID_SHOW:
			showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
			win.SetForegroundWindow(webviewHwnd)
		case ID_QUIT:
			win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
			if device != nil {
				device.Close()
			}
			os.Exit(0)
		}
	}
	return win.DefWindowProc(hwnd, msg, wParam, lParam)
}

func showMenu() {
	hMenu := win.CreatePopupMenu()
	if hMenu == 0 {
		return
	}

	batteryItem, _ := syscall.UTF16PtrFromString(batteryText)
	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING|win.MF_GRAYED), 0, uintptr(unsafe.Pointer(batteryItem)))

	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_SEPARATOR), 0, 0)

	showItem, _ := syscall.UTF16PtrFromString("Show Window")
	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_SHOW, uintptr(unsafe.Pointer(showItem)))

	quitItem, _ := syscall.UTF16PtrFromString("Quit")
	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_QUIT, uintptr(unsafe.Pointer(quitItem)))

	var pt win.POINT
	win.GetCursorPos(&pt)
	win.SetForegroundWindow(hwnd)

	trackPopupMenu := user32.NewProc("TrackPopupMenu")
	trackPopupMenu.Call(uintptr(hMenu), uintptr(win.TPM_RIGHTBUTTON), uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)

	win.DestroyMenu(hMenu)
}

func updateBattery() {
	hid.Init()
	defer hid.Exit()

	interval := time.Duration(settings.RefreshInterval) * time.Second
	if interval < 1*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		reconnect()

		if device != nil {
			battery, charging := readBattery()
			if battery > 0 {
				// Detect charge completion
				if wasCharging && !charging && battery >= 95 {
					lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
					lastChargeLevel = battery
					saveChargeData()
				}
				
				batteryLvl = battery
				wasCharging = charging
				isCharging = charging
				status := "Discharging"
				icon := "🔋"
				if charging {
					status = "Charging"
					icon = "⚡"
				}
				batteryText = fmt.Sprintf("%s %d%% (%s)", icon, battery, status)
				updateTrayTooltip(fmt.Sprintf("Battery: %d%%", battery))
				updateTrayIcon(battery, charging)

				broadcast(map[string]interface{}{
					"level":           battery,
					"charging":        charging,
					"status":          status,
					"lastChargeTime":  lastChargeTime,
					"lastChargeLevel": lastChargeLevel,
					"deviceModel":     deviceModel,
				})
			}
		} else {
			batteryLvl = 0
			batteryText = "Mouse Not Found"
			updateTrayTooltip("Mouse Not Found")
			broadcast(map[string]interface{}{"level": 0, "charging": false, "status": "disconnected"})
		}

		<-ticker.C
	}
}

func updateTrayTooltip(text string) {
	tip, _ := syscall.UTF16FromString(text)
	copy(nid.SzTip[:], tip)
	win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
}

func reconnect() {
	if device != nil {
		return
	}

	for _, pid := range supportedDevices {
		var paths []string
		hid.Enumerate(VendorID, pid, func(info *hid.DeviceInfo) error {
			paths = append(paths, info.Path)
			return nil
		})

		for _, path := range paths {
			d, err := hid.OpenPath(path)
			if err != nil {
				continue
			}

			battery, _ := testBattery(d)
			if battery > 0 {
				device = d
				if name, ok := deviceNames[pid]; ok {
					deviceModel = name
				}
				return
			}
			d.Close()
		}
	}
}

func testBattery(d *hid.Device) (int, bool) {
	command := []byte{0x00, 0x00, 0x00, 0x02, 0x02, 0x00, 0x83}
	outputReport := make([]byte, 65)
	copy(outputReport, command)

	d.SendFeatureReport(outputReport)
	time.Sleep(100 * time.Millisecond)

	inputReport := make([]byte, 65)
	n, _ := d.GetFeatureReport(inputReport)

	if n >= 9 && inputReport[6] == 0x83 {
		return int(inputReport[8]), inputReport[7] == 1
	}
	return 0, false
}

func readBattery() (int, bool) {
	if device == nil {
		return 0, false
	}

	command := []byte{0x00, 0x00, 0x00, 0x02, 0x02, 0x00, 0x83}
	outputReport := make([]byte, 65)
	copy(outputReport, command)

	if _, err := device.SendFeatureReport(outputReport); err != nil {
		device.Close()
		device = nil
		return 0, false
	}

	time.Sleep(100 * time.Millisecond)

	inputReport := make([]byte, 65)
	n, err := device.GetFeatureReport(inputReport)
	if err != nil {
		device.Close()
		device = nil
		return 0, false
	}

	if n >= 9 && inputReport[6] == 0x83 {
		return int(inputReport[8]), inputReport[7] == 1
	}
	return 0, false
}

func createBatteryIcon(level int, charging bool) win.HICON {
	// Get system metrics for icon size
	getSystemMetrics := user32.NewProc("GetSystemMetrics")
	smCxIcon, _, _ := getSystemMetrics.Call(uintptr(11)) // SM_CXICON
	smCyIcon, _, _ := getSystemMetrics.Call(uintptr(12)) // SM_CYICON

	// Use larger size for better visibility (2x the system icon size)
	width := int32(smCxIcon) * 2
	height := int32(smCyIcon) * 2
	if width < 64 {
		width = 64
		height = 64
	}

	// Create BITMAPV5HEADER for 32-bit ARGB
	type BITMAPV5HEADER struct {
		Size          uint32
		Width         int32
		Height        int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPelsPerMeter int32
		YPelsPerMeter int32
		ClrUsed       uint32
		ClrImportant  uint32
		RedMask       uint32
		GreenMask     uint32
		BlueMask      uint32
		AlphaMask     uint32
		CSType        uint32
		Endpoints     [36]byte
		GammaRed      uint32
		GammaGreen    uint32
		GammaBlue     uint32
		Intent        uint32
		ProfileData   uint32
		ProfileSize   uint32
		Reserved      uint32
	}

	bi := BITMAPV5HEADER{
		Size:        uint32(unsafe.Sizeof(BITMAPV5HEADER{})),
		Width:       width,
		Height:      -height, // Negative for top-down
		Planes:      1,
		BitCount:    32,
		Compression: 3, // BI_BITFIELDS
		RedMask:     0x00FF0000,
		GreenMask:   0x0000FF00,
		BlueMask:    0x000000FF,
		AlphaMask:   0xFF000000,
	}

	hdc := win.GetDC(0)
	defer win.ReleaseDC(0, hdc)

	var pBits unsafe.Pointer
	gdi32 := syscall.NewLazyDLL("gdi32.dll")
	createDIBSection := gdi32.NewProc("CreateDIBSection")

	hBitmap, _, _ := createDIBSection.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(&bi)),
		0, // DIB_RGB_COLORS
		uintptr(unsafe.Pointer(&pBits)),
		0,
		0,
	)

	if hBitmap == 0 || pBits == nil {
		return 0
	}

	// Draw battery icon in ARGB buffer
	size := width * height
	pixelSlice := unsafe.Slice((*uint32)(pBits), size)

	// Clear to transparent
	for i := range pixelSlice {
		pixelSlice[i] = 0x00000000
	}

	// Determine fill color
	var fillColor uint32
	if charging {
		fillColor = 0xFF0078D4 // Blue
	} else if level >= 50 {
		fillColor = 0xFF107C10 // Green
	} else if level >= 20 {
		fillColor = 0xFFF7630C // Orange
	} else {
		fillColor = 0xFFC42B1C // Red
	}

	black := uint32(0xFF000000)
	white := uint32(0xFFFFFFFF)

	// Scale coordinates based on icon size
	scale := float32(width) / 64.0

	// Battery body coordinates (scaled)
	bodyLeft := int32(float32(10) * scale)
	bodyTop := int32(float32(20) * scale)
	bodyRight := int32(float32(50) * scale)
	bodyBottom := int32(float32(44) * scale)

	// Draw battery body
	for y := bodyTop; y <= bodyBottom; y++ {
		for x := bodyLeft; x <= bodyRight; x++ {
			if y == bodyTop || y == bodyBottom || x == bodyLeft || x == bodyRight {
				pixelSlice[y*width+x] = black // Outline
			} else {
				pixelSlice[y*width+x] = white // Inside
			}
		}
	}

	// Draw battery tip
	tipLeft := bodyRight + 1
	tipRight := int32(float32(56) * scale)
	tipTop := int32(float32(27) * scale)
	tipBottom := int32(float32(37) * scale)
	for y := tipTop; y <= tipBottom; y++ {
		for x := tipLeft; x <= tipRight; x++ {
			pixelSlice[y*width+x] = black
		}
	}

	// Fill battery based on level
	if level > 0 {
		fillWidth := int32(float32(38) * scale * float32(level) / 100.0)
		for y := bodyTop + 1; y < bodyBottom; y++ {
			for x := bodyLeft + 1; x < bodyLeft+1+fillWidth; x++ {
				if x < bodyRight {
					pixelSlice[y*width+x] = fillColor
				}
			}
		}
	}

	// Create icon from bitmap
	var iconInfo win.ICONINFO
	iconInfo.FIcon = 1
	iconInfo.HbmColor = win.HBITMAP(hBitmap)
	iconInfo.HbmMask = win.HBITMAP(hBitmap)

	hIcon := win.CreateIconIndirect(&iconInfo)
	win.DeleteObject(win.HGDIOBJ(hBitmap))

	return hIcon
}

func updateTrayIcon(level int, charging bool) {
	oldIcon := nid.HIcon
	nid.HIcon = createBatteryIcon(level, charging)
	win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
	if oldIcon != 0 {
		win.DestroyIcon(oldIcon)
	}
}

func loadChargeData() {
	data, err := os.ReadFile(dataFile)
	if err != nil {
		return
	}
	var cd ChargeData
	if err := json.Unmarshal(data, &cd); err == nil {
		lastChargeTime = cd.LastChargeTime
		lastChargeLevel = cd.LastChargeLevel
	}
}

func saveChargeData() {
	cd := ChargeData{
		LastChargeTime:  lastChargeTime,
		LastChargeLevel: lastChargeLevel,
	}
	data, err := json.MarshalIndent(cd, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(dataFile, data, 0644)
}

func loadSettings() {
	// Default settings
	settings = Settings{
		StartWithWindows: false,
		RefreshInterval:  5,
	}
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return
	}
	json.Unmarshal(data, &settings)
}

func saveSettings() {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(settingsFile, data, 0644)
	
	// Apply startup setting
	if settings.StartWithWindows {
		enableStartup()
	} else {
		disableStartup()
	}
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	if r.Method == "GET" {
		json.NewEncoder(w).Encode(settings)
		return
	}
	
	if r.Method == "POST" {
		var newSettings Settings
		if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		settings = newSettings
		saveSettings()
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}
}

func enableStartup() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	regSetValueEx := advapi32.NewProc("RegSetValueExW")
	
	key, _ := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Run`)
	var handle syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, key, 0, syscall.KEY_WRITE, &handle) == nil {
		defer syscall.RegCloseKey(handle)
		valueName, _ := syscall.UTF16PtrFromString("GloriousBatteryMonitor")
		valueData, _ := syscall.UTF16FromString(exePath)
		regSetValueEx.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(valueName)),
			0,
			uintptr(syscall.REG_SZ),
			uintptr(unsafe.Pointer(&valueData[0])),
			uintptr(len(valueData)*2),
		)
	}
}

func disableStartup() {
	advapi32 := syscall.NewLazyDLL("advapi32.dll")
	regDeleteValue := advapi32.NewProc("RegDeleteValueW")
	
	key, _ := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Run`)
	var handle syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, key, 0, syscall.KEY_WRITE, &handle) == nil {
		defer syscall.RegCloseKey(handle)
		valueName, _ := syscall.UTF16PtrFromString("GloriousBatteryMonitor")
		regDeleteValue.Call(uintptr(handle), uintptr(unsafe.Pointer(valueName)))
	}
}
