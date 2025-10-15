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
	ID_UPDATE   = 1003
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
	LastChargeTime  string  `json:"lastChargeTime"`
	LastChargeLevel int     `json:"lastChargeLevel"`
	DischargeRate   float64 `json:"dischargeRate"`
	ChargeRate      float64 `json:"chargeRate"`
	Timestamp       string  `json:"timestamp"`
}

type Settings struct {
	StartWithWindows         bool `json:"startWithWindows"`
	StartMinimized           bool `json:"startMinimized"`
	RefreshInterval          int  `json:"refreshInterval"` // in seconds
	NotificationsEnabled     bool `json:"notificationsEnabled"`
	LowBatteryThreshold      int  `json:"lowBatteryThreshold"`      // percentage
	CriticalBatteryThreshold int  `json:"criticalBatteryThreshold"` // percentage
}

const currentVersion = "2.2.3"

var (
	device            *hid.Device
	deviceModel       string = "Unknown"
	hwnd              win.HWND
	webviewHwnd       win.HWND
	nid               win.NOTIFYICONDATA
	batteryText       string = "Connecting..."
	batteryLvl        int
	isCharging        bool
	wasCharging       bool
	lastChargeTime    string = "Never"
	lastChargeLevel   int    = 0
	user32                   = syscall.NewLazyDLL("user32.dll")
	appendMenuW              = user32.NewProc("AppendMenuW")
	setWindowLong            = user32.NewProc("SetWindowLongPtrW")
	showWindow               = user32.NewProc("ShowWindow")
	clients                  = make(map[chan string]bool)
	w                 webview2.WebView
	serverPort        string = "8765"
	dataFile          string
	settingsFile      string
	settings          Settings
	notifiedLow       bool
	notifiedCritical  bool
	notifiedFull      bool
	lastBatteryLevel  int = -1
	lastBatteryTime   time.Time
	dischargeRate     float64 = 0
	lastChargeLevel2  int     = -1
	lastChargeTime2   time.Time
	chargeRate        float64 = 0
	rateHistory       []float64
	chargeRateHistory []float64
	animationFrame    int = 0
	stopAnimation     chan bool
	updateAvailable   bool
	updateVersion     string
	updateURL         string
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

	// Fix startup registry path if needed
	if settings.StartWithWindows {
		enableStartup()
	}

	// Check for updates in background
	go checkForUpdates()

	// Allow overriding the embedded web server port via PORT env var (useful for debugging or port conflicts)
	if p := os.Getenv("PORT"); p != "" {
		serverPort = p
	}

	stopAnimation = make(chan bool)
	go startWebServer()
	go startTray()
	go updateBattery()
	go animateChargingIcon()

	time.Sleep(500 * time.Millisecond)

	// Memory optimization: Set WebView2 browser arguments via environment variable
	// Reduces memory usage by ~40-50MB with minimal performance impact
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--disable-gpu --disable-software-rasterizer --disable-extensions --disable-background-networking --disk-cache-size=1 --media-cache-size=1 --disable-features=AudioServiceOutOfProcess")

	w = webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Glorious Mouse Battery Monitor",
			Width:  520,
			Height: 700,
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

	// Disable window resizing
	style := win.GetWindowLongPtr(webviewHwnd, win.GWL_STYLE)
	style &^= win.WS_THICKFRAME | win.WS_MAXIMIZEBOX
	win.SetWindowLongPtr(webviewHwnd, win.GWL_STYLE, style)

	oldProc := win.SetWindowLongPtr(webviewHwnd, win.GWLP_WNDPROC, syscall.NewCallback(webviewWndProc))
	win.SetWindowLongPtr(webviewHwnd, win.GWLP_USERDATA, oldProc)

	w.Navigate(fmt.Sprintf("http://localhost:%s", serverPort))

	// Start minimized if setting is enabled
	if settings.StartMinimized {
		showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_HIDE))
	}

	w.Run()
}

func startWebServer() {
	http.HandleFunc("/", serveHTML)
	http.HandleFunc("/events", handleSSE)
	http.HandleFunc("/api/settings", handleSettings)
	http.HandleFunc("/api/update", handleUpdate)
	http.HandleFunc("/api/resize", handleResize)
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
	nid.HIcon = createBatteryIcon(0, false, 0)

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

	if updateAvailable {
		updateText := fmt.Sprintf("🚀 Update Available (v%s)", updateVersion)
		updateItem, _ := syscall.UTF16PtrFromString(updateText)
		appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_UPDATE, uintptr(unsafe.Pointer(updateItem)))
	}

	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_SEPARATOR), 0, 0)

	showItem, _ := syscall.UTF16PtrFromString("Show Window")
	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_SHOW, uintptr(unsafe.Pointer(showItem)))

	quitItem, _ := syscall.UTF16PtrFromString("Quit")
	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_QUIT, uintptr(unsafe.Pointer(quitItem)))

	var pt win.POINT
	win.GetCursorPos(&pt)
	win.SetForegroundWindow(hwnd)

	trackPopupMenu := user32.NewProc("TrackPopupMenu")
	cmd, _, _ := trackPopupMenu.Call(
		uintptr(hMenu),
		uintptr(win.TPM_RETURNCMD|win.TPM_RIGHTBUTTON),
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		uintptr(hwnd),
		0,
	)

	win.DestroyMenu(hMenu)

	// Handle command immediately
	if cmd == ID_SHOW {
		showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
		win.SetForegroundWindow(webviewHwnd)
	} else if cmd == ID_UPDATE {
		showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
		win.SetForegroundWindow(webviewHwnd)
	} else if cmd == ID_QUIT {
		win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
		if device != nil {
			device.Close()
		}
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		terminateProcess := kernel32.NewProc("TerminateProcess")
		getCurrentProcess := kernel32.NewProc("GetCurrentProcess")
		handle, _, _ := getCurrentProcess.Call()
		terminateProcess.Call(handle, 0)
	}
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

				// Reset notification flags when charging
				if charging {
					notifiedLow = false
					notifiedCritical = false
					if battery == 100 && !notifiedFull {
						sendNotification("Battery Fully Charged", "Your mouse is now at 100% battery", false)
						notifiedFull = true
					}
					// Initialize tracking on first charge reading
					if lastChargeLevel2 < 0 {
						lastChargeLevel2 = battery
						lastChargeTime2 = time.Now()
					} else if battery > lastChargeLevel2 {
						// Only update rate if we have at least 3% of data for accuracy
						if (battery - lastChargeLevel2) >= 3 {
							elapsed := time.Since(lastChargeTime2).Hours()
							if elapsed > 0 {
								newRate := float64(battery-lastChargeLevel2) / elapsed
								chargeRateHistory = append(chargeRateHistory, newRate)
								if len(chargeRateHistory) > 5 {
									chargeRateHistory = chargeRateHistory[1:]
								}
								chargeRate = calculateEMA(chargeRateHistory)
								lastChargeLevel2 = battery
								lastChargeTime2 = time.Now()
								saveChargeData() // Save rates after update
							}
						}
					}
				} else {
					notifiedFull = false
					// Initialize tracking on first discharge reading
					if lastBatteryLevel < 0 {
						lastBatteryLevel = battery
						lastBatteryTime = time.Now()
					} else if lastBatteryLevel > battery {
						// Only update rate if we have at least 3% of data for accuracy
						if (lastBatteryLevel - battery) >= 3 {
							elapsed := time.Since(lastBatteryTime).Hours()
							if elapsed > 0 {
								newRate := float64(lastBatteryLevel-battery) / elapsed
								rateHistory = append(rateHistory, newRate)
								if len(rateHistory) > 5 {
									rateHistory = rateHistory[1:]
								}
								dischargeRate = calculateEMA(rateHistory)
								lastBatteryLevel = battery
								lastBatteryTime = time.Now()
								saveChargeData() // Save rates after update
							}
						}
					}
				}

				// Send notifications only once per threshold
				if settings.NotificationsEnabled && !charging {
					if battery <= settings.CriticalBatteryThreshold && !notifiedCritical {
						notifiedCritical = true
						sendNotification("Critical Battery", fmt.Sprintf("Battery at %d%%. Please charge soon!", battery), true)
					} else if battery <= settings.LowBatteryThreshold && !notifiedLow {
						notifiedLow = true
						sendNotification("Low Battery", fmt.Sprintf("Battery at %d%%. Consider charging.", battery), false)
					}
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

				// Calculate time remaining every tick
				timeRemaining := ""
				if !charging && dischargeRate > 0 && battery > 0 {
					hoursLeft := float64(battery) / dischargeRate
					if hoursLeft < 100 && hoursLeft > 0 {
						hours := int(hoursLeft)
						minutes := int((hoursLeft - float64(hours)) * 60)
						if hours >= 24 {
							days := hours / 24
							remainingHours := hours % 24
							if remainingHours > 0 {
								timeRemaining = fmt.Sprintf("%dd %dh", days, remainingHours)
							} else {
								timeRemaining = fmt.Sprintf("%dd", days)
							}
						} else if hours > 0 {
							timeRemaining = fmt.Sprintf("%dh %dm", hours, minutes)
						} else if minutes > 0 {
							timeRemaining = fmt.Sprintf("%dm", minutes)
						}
					}
				} else if charging && chargeRate > 0 && battery < 100 {
					hoursLeft := float64(100-battery) / chargeRate
					if hoursLeft < 100 && hoursLeft > 0 {
						hours := int(hoursLeft)
						minutes := int((hoursLeft - float64(hours)) * 60)
						if hours >= 24 {
							days := hours / 24
							remainingHours := hours % 24
							if remainingHours > 0 {
								timeRemaining = fmt.Sprintf("%dd %dh", days, remainingHours)
							} else {
								timeRemaining = fmt.Sprintf("%dd", days)
							}
						} else if hours > 0 {
							timeRemaining = fmt.Sprintf("%dh %dm", hours, minutes)
						} else if minutes > 0 {
							timeRemaining = fmt.Sprintf("%dm", minutes)
						}
					}
				}
				broadcast(map[string]interface{}{
					"level":           battery,
					"charging":        charging,
					"status":          status,
					"lastChargeTime":  lastChargeTime,
					"lastChargeLevel": lastChargeLevel,
					"deviceModel":     deviceModel,
					"timeRemaining":   timeRemaining,
					"updateAvailable": updateAvailable,
					"updateVersion":   updateVersion,
				})
			}
		} else {
			batteryLvl = 0
			isCharging = false
			batteryText = "Mouse Not Found"
			updateTrayTooltip("Mouse Not Found")
			updateTrayIcon(0, false) // Update icon to show disconnected state
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

func createBatteryIcon(level int, charging bool, frame int) win.HICON {
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

	// Fill battery based on level with animation for charging
	if level > 0 {
		displayLevel := level
		if charging {
			// Pulsing animation: cycle through +0%, +10%, +20%
			displayLevel = level + (frame * 10)
			if displayLevel > 100 {
				displayLevel = 100
			}
		}
		fillWidth := int32(float32(38) * scale * float32(displayLevel) / 100.0)
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
	nid.HIcon = createBatteryIcon(level, charging, animationFrame)
	win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
	if oldIcon != 0 {
		win.DestroyIcon(oldIcon)
	}
}

func animateChargingIcon() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if isCharging {
				animationFrame = (animationFrame + 1) % 3
				updateTrayIcon(batteryLvl, isCharging)
			} else {
				animationFrame = 0
			}
		case <-stopAnimation:
			return
		}
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

		// Load battery rates if data is fresh (< 24 hours old)
		if cd.Timestamp != "" {
			if savedTime, err := time.Parse(time.RFC3339, cd.Timestamp); err == nil {
				if time.Since(savedTime) < 24*time.Hour {
					dischargeRate = cd.DischargeRate
					chargeRate = cd.ChargeRate
				}
			}
		}
	}
}

func saveChargeData() {
	cd := ChargeData{
		LastChargeTime:  lastChargeTime,
		LastChargeLevel: lastChargeLevel,
		DischargeRate:   dischargeRate,
		ChargeRate:      chargeRate,
		Timestamp:       time.Now().Format(time.RFC3339),
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
		StartWithWindows:         false,
		StartMinimized:           false,
		RefreshInterval:          5,
		NotificationsEnabled:     false,
		LowBatteryThreshold:      20,
		CriticalBatteryThreshold: 10,
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

	// Apply startup setting (always updates path to current location)
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

func handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !updateAvailable || updateURL == "" {
		http.Error(w, "No update available", http.StatusBadRequest)
		return
	}

	go func() {
		if err := downloadAndInstallUpdate(updateURL); err != nil {
			log.Printf("Update failed: %v", err)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleResize(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Height int `json:"height"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rect win.RECT
	win.GetWindowRect(webviewHwnd, &rect)
	win.SetWindowPos(webviewHwnd, 0, 0, 0, rect.Right-rect.Left, int32(req.Height), win.SWP_NOMOVE|win.SWP_NOZORDER)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
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

func calculateEMA(rates []float64) float64 {
	if len(rates) == 0 {
		return 0
	}
	if len(rates) == 1 {
		return rates[0]
	}
	// Use exponential moving average with alpha=0.3 (similar to phones)
	alpha := 0.3
	ema := rates[0]
	for i := 1; i < len(rates); i++ {
		ema = alpha*rates[i] + (1-alpha)*ema
	}
	return ema
}

func sendNotification(title, message string, critical bool) {
	log.Printf("Sending notification: %s - %s", title, message)

	// Send Windows notification via system tray
	nid.UFlags = win.NIF_INFO
	nid.DwInfoFlags = win.NIIF_INFO
	if critical {
		nid.DwInfoFlags = win.NIIF_WARNING
	}

	infoTitle, _ := syscall.UTF16FromString(title)
	copy(nid.SzInfoTitle[:], infoTitle)

	infoText, _ := syscall.UTF16FromString(message)
	copy(nid.SzInfo[:], infoText)

	win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)

	// Reset flags
	time.Sleep(100 * time.Millisecond)
	nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
	win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
}

type GitHubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func checkForUpdates() {
	// Wait 5 seconds before checking (let app start first)
	time.Sleep(5 * time.Second)

	resp, err := http.Get("https://api.github.com/repos/Rodrigo-200/GloriousBatteryMonitor/releases/latest")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return
	}

	// Remove 'v' prefix from tag for comparison
	latestVersion := release.TagName
	if len(latestVersion) > 0 && latestVersion[0] == 'v' {
		latestVersion = latestVersion[1:]
	}

	if latestVersion != currentVersion {
		// Find the .exe asset
		var downloadURL string
		for _, asset := range release.Assets {
			if asset.Name == "GloriousBatteryMonitor.exe" {
				downloadURL = asset.BrowserDownloadURL
				break
			}
		}

		if downloadURL != "" {
			go promptUpdate(latestVersion, downloadURL)
		}
	}
}

func promptUpdate(version, downloadURL string) {
	updateAvailable = true
	updateVersion = version
	updateURL = downloadURL

	// Show notification
	message := fmt.Sprintf("Version %s is available. Open the app to update.", version)
	sendNotification("Update Available", message, false)

	// Broadcast to UI clients
	broadcast(map[string]interface{}{
		"updateAvailable": true,
		"updateVersion":   version,
	})
}

func downloadAndInstallUpdate(downloadURL string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	tempFile := exePath + ".new"
	resp, err := http.Get(downloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(tempFile)
	if err != nil {
		return err
	}

	if _, err := out.ReadFrom(resp.Body); err != nil {
		out.Close()
		return err
	}
	out.Close()

	// Use native Windows API to replace file on next reboot (no cmd.exe)
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	moveFileEx := kernel32.NewProc("MoveFileExW")
	
	exePathW, _ := syscall.UTF16PtrFromString(exePath)
	tempFileW, _ := syscall.UTF16PtrFromString(tempFile)
	
	// MOVEFILE_REPLACE_EXISTING (0x1) | MOVEFILE_DELAY_UNTIL_REBOOT (0x4)
	moveFileEx.Call(
		uintptr(unsafe.Pointer(tempFileW)),
		uintptr(unsafe.Pointer(exePathW)),
		uintptr(0x1|0x4),
	)

	// Restart the application
	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExecute := shell32.NewProc("ShellExecuteW")
	verb, _ := syscall.UTF16PtrFromString("open")
	shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exePathW)), 0, 0, 1)

	// Exit current process
	terminateProcess := kernel32.NewProc("TerminateProcess")
	getCurrentProcess := kernel32.NewProc("GetCurrentProcess")
	handle, _, _ := getCurrentProcess.Call()
	terminateProcess.Call(handle, 0)

	return nil
}
