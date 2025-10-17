package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/jchv/go-webview2"
	"github.com/lxn/win"
	"github.com/sstallion/go-hid"
)

//go:embed ui.html
var content embed.FS

const (
	WM_APP           = 0x8000
	WM_APP_TRAY_MSG  = WM_APP + 10 // callback from Shell_NotifyIcon
	WM_APP_TRAY_DO   = WM_APP + 1
	WM_APP_ICON_REAP = WM_APP + 2

	ID_SHOW   = 1001
	ID_QUIT   = 1002
	ID_UPDATE = 1003

	WM_MOUSEMOVE     = 0x0200
	WM_LBUTTONUP     = 0x0202
	WM_LBUTTONDBLCLK = 0x0203
	WM_RBUTTONUP     = 0x0205
	WM_CONTEXTMENU   = 0x007B

	NIN_SELECT       = win.WM_USER + 0 // left-click (NotifyIcon v4)
	NIN_KEYSELECT    = win.WM_USER + 1
	NIN_BALLOONCLICK = win.WM_USER + 5
	NIN_POPUPOPEN    = win.WM_USER + 0x006
	NIN_POPUPCLOSE   = win.WM_USER + 0x007
)

var gloriousVendorIDs = []uint16{
	0x258a, // older Glorious
	0x342d, // newer Glorious (keyboards, some mice)
	0x093a, // PixArt (wireless dongles for many Glorious mice)
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
	0x824d: "Model D2 Wireless (PixArt Dongle)",
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

type DeviceProfile struct {
	Path            string `json:"path"`
	ReportID        byte   `json:"reportId"`
	ReportLen       int    `json:"reportLen"`
	UseGetOnly      bool   `json:"useGetOnly"`
	UseInputReports bool   `json:"useInputReports"`
}

const currentVersion = "2.3.0"

var (
	device               *hid.Device
	deviceModel          string = "Unknown"
	hwnd                 win.HWND
	webviewHwnd          win.HWND
	nid                  win.NOTIFYICONDATA
	batteryText          string = "Connecting..."
	batteryLvl           int
	isCharging           bool
	wasCharging          bool
	hasPrevCharging      bool
	lastChargeTime       string       = "Never"
	lastChargeLevel      int          = 0
	user32                            = syscall.NewLazyDLL("user32.dll")
	appendMenuW                       = user32.NewProc("AppendMenuW")
	showWindow                        = user32.NewProc("ShowWindow")
	clients                           = make(map[chan string]bool)
	clientsMu            sync.RWMutex // ← add
	w                    webview2.WebView
	serverPort           string = "8765"
	dataFile             string
	settingsFile         string
	logFile              string
	logger               *log.Logger
	settings             Settings
	notifiedLow          bool
	notifiedCritical     bool
	notifiedFull         bool
	lastBatteryLevel     int = -1
	lastBatteryTime      time.Time
	dischargeRate        float64 = 0
	lastChargeLevel2     int     = -1
	lastChargeTime2      time.Time
	chargeRate           float64 = 0
	rateHistory          []float64
	chargeRateHistory    []float64
	animationFrame       int = 0
	stopAnimation        chan bool
	updateAvailable      bool
	updateVersion        string
	updateURL            string
	selectedReportID     byte = 0x00
	selectedReportLen    int  = 65
	useGetOnly           bool
	consecutiveReadFails int
	linkDown             bool
	probeRIDs            = []byte{0x00, 0x02, 0x04, 0x03}
	useInputReports      bool
	inputFrames          chan []byte
	cacheFile            string
	cachedProfile        *DeviceProfile
	softLinkDownCount    int
	currentHIDPath       string
	fileMu               sync.Mutex
	safeForInput         bool
	inputDev             *hid.Device
	inputMu              sync.Mutex
	recordedUnplug       bool
	trayMu               sync.Mutex
	trayOps              = make(chan func(), 64)
	iconReap             = make(chan win.HICON, 64)
	readerDone           chan struct{}
	taskbarCreated       = win.RegisterWindowMessage(syscall.StringToUTF16Ptr("TaskbarCreated"))
)

func safeDefer(where string) {
	if r := recover(); r != nil {
		if logger != nil {
			logger.Printf("[RECOVER] %s: %v", where, r)
		}
	}
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("[FATAL RECOVER] %v\n%s", r, debug.Stack())
			} else {
				log.Printf("[FATAL RECOVER] %v\n%s", r, debug.Stack())
			}
		}
	}()
	// Set up data file paths
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = "."
	}
	dataDir := filepath.Join(appData, "GloriousBatteryMonitor")
	os.MkdirAll(dataDir, 0755)
	dataFile = filepath.Join(dataDir, "charge_data.json")
	settingsFile = filepath.Join(dataDir, "settings.json")
	logFile = filepath.Join(dataDir, "debug.log")
	cacheFile = filepath.Join(dataDir, "conn_profile.json")

	// Set up logging
	setupLogging()

	// Load saved data
	loadChargeData()
	loadSettings()
	loadConnProfile()

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

func ensureInputReader(d *hid.Device) {
	if !safeForInput || d == nil {
		return
	}

	// Already running for this device?
	inputMu.Lock()
	if inputFrames != nil && inputDev == d {
		inputMu.Unlock()
		return
	}

	// Replace the channel/owner
	ch := make(chan []byte, 16)
	done := make(chan struct{})
	inputFrames = ch
	inputDev = d
	readerDone = done
	inputMu.Unlock()

	go func(dev *hid.Device, out chan []byte) {
		defer func() {
			if r := recover(); r != nil {
				if logger != nil {
					logger.Printf("input reader recovered: %v", r)
				}
			}
			close(out)

			inputMu.Lock()
			if inputFrames == out {
				inputFrames = nil
				inputDev = nil
			}
			// always signal completion
			if readerDone != nil {
				close(done)
				if readerDone == done {
					readerDone = nil
				}
			}
			inputMu.Unlock()
		}()

		buf := make([]byte, 65)
		for {
			n, err := dev.Read(buf) // blocking
			if err != nil {
				return
			}
			if n > 0 {
				frame := make([]byte, n)
				copy(frame, buf[:n])
				select {
				case out <- frame:
				default:
				}
			} else {
				time.Sleep(30 * time.Millisecond)
			}
		}
	}(d, ch)
}

func trayInvoke(fn func()) {
	// drop if completely flooded; we don't want to block battery loop
	select {
	case trayOps <- fn:
	default:
	}
	if hwnd != 0 {
		win.PostMessage(hwnd, WM_APP_TRAY_DO, 0, 0)
	}
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

	if f, ok := w.(http.Flusher); ok {
		fmt.Fprint(w, ":ok\n\n")
		f.Flush()
	}

	messageChan := make(chan string, 8)

	clientsMu.Lock()
	clients[messageChan] = true
	clientsMu.Unlock()

	// Clean up this client safely
	defer func() {
		clientsMu.Lock()
		delete(clients, messageChan)
		// IMPORTANT: close WHILE holding the write lock so broadcasters can't be reading/sending
		close(messageChan)
		clientsMu.Unlock()
	}()

	flusher, _ := w.(http.Flusher)

	// Exit when the client goes away so we don’t leak goroutines
	ctxDone := r.Context().Done()

	for {
		select {
		case <-ctxDone:
			return
		case msg, ok := <-messageChan:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func broadcast(data map[string]interface{}) {
	jsonData, _ := json.Marshal(data)
	payload := string(jsonData)

	clientsMu.RLock()
	for client := range clients {
		func(ch chan string, m string) {
			defer func() {
				if r := recover(); r != nil {
					if logger != nil {
						logger.Printf("[SSE] dropped send to closed channel: %v", r)
					}
				}
			}()
			select {
			case ch <- m:
			default:
				// buffer full; drop rather than blocking
			}
		}(client, payload)
	}
	clientsMu.RUnlock()
}

func startTray() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

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

	// --- tray icon init ---
	nid = win.NOTIFYICONDATA{}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1

	// Add a fixed GUID so Explorer treats the icon as persistent across restarts
	nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP

	nid.UCallbackMessage = WM_APP_TRAY_MSG
	nid.HIcon = createBatteryIcon(0, false, 0)
	tip, _ := syscall.UTF16FromString("Glorious Battery")
	copy(nid.SzTip[:], tip)

	// now add the icon
	win.Shell_NotifyIcon(win.NIM_ADD, &nid)
	nid.UVersion = win.NOTIFYICON_VERSION_4
	win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)
	updateTrayTooltip("Glorious Battery")
	// Reap old HICONs periodically
	go func() {
		t := time.NewTicker(1500 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			if hwnd != 0 {
				win.PostMessage(hwnd, WM_APP_ICON_REAP, 0, 0)
			}
		}
	}()

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
	if msg == taskbarCreated {
		win.Shell_NotifyIcon(win.NIM_ADD, &nid)
		nid.UVersion = win.NOTIFYICON_VERSION_4
		win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)
		updateTrayTooltip(batteryText) // refresh current text
		return 0
	}

	switch msg {
	case WM_APP_TRAY_MSG:
		// LOWORD(lParam) is the actual message code
		code := uint32(lParam) & 0xFFFF
		log.Printf("TRAY: wParam=%d lParam=0x%X (code=0x%X)", wParam, lParam, code)

		switch code {
		// Left click / select
		case NIN_SELECT, NIN_KEYSELECT, WM_LBUTTONUP, WM_LBUTTONDBLCLK:
			// restore if minimized/hidden, then foreground
			win.ShowWindow(webviewHwnd, win.SW_RESTORE)
			win.SetForegroundWindow(webviewHwnd)
			return 0

		// Right click / context menu
		case WM_RBUTTONUP, WM_CONTEXTMENU:
			showMenu()
			return 0

		// Optional: overflow flyout open/close if you care
		case NIN_POPUPOPEN, NIN_POPUPCLOSE:
			return 0
		}
		return 0

	case win.WM_CONTEXTMENU:
		showMenu()
		return 0

	case WM_APP_TRAY_DO:
		for {
			select {
			case fn := <-trayOps:
				func() {
					defer func() {
						if r := recover(); r != nil && logger != nil {
							logger.Printf("[TRAY_OP RECOVER] %v", r)
						}
					}()
					fn()
				}()
			default:
				return 0
			}
		}

	case WM_APP_ICON_REAP:
		for i := 0; i < 8; i++ {
			select {
			case h := <-iconReap:
				if h != 0 {
					win.DestroyIcon(h)
				}
			default:
				return 0
			}
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

	postMessage := user32.NewProc("PostMessageW")
	postMessage.Call(uintptr(hwnd), 0, 0, 0) // WM_NULL

	win.DestroyMenu(hMenu)

	switch cmd {
	case ID_SHOW, ID_UPDATE:
		showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
		win.SetForegroundWindow(webviewHwnd)
	case ID_QUIT:
		trayInvoke(func() { win.Shell_NotifyIcon(win.NIM_DELETE, &nid) })
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
	defer safeDefer("updateBattery")
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("updateBattery recovered from panic: %v", r)
			}
			// Small backoff then restart the loop by calling updateBattery again.
			// NOTE: this returns from the current goroutine; the caller started it with `go updateBattery()`.
			go updateBattery()
		}
	}()

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

			if linkDown {
				softLinkDownCount++
				if wasCharging && softLinkDownCount == 1 && !recordedUnplug {
					// choose a sensible last-known level:
					// 1) current on-screen level if >0, else
					// 2) last charging track level if we have one, else
					// 3) keep previous lastChargeLevel
					lvl := batteryLvl
					if lvl <= 0 && lastChargeLevel2 > 0 {
						lvl = lastChargeLevel2
					}
					if lvl > 0 {
						lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
						lastChargeLevel = lvl
						saveChargeData()
						recordedUnplug = true
					}
				}
				if softLinkDownCount == 1 {
					// transient
					updateTrayTooltip("Reconnecting…")
					broadcast(map[string]interface{}{"status": "connecting", "statusText": "Connecting"})
				} else {
					batteryLvl = 0
					isCharging = false
					batteryText = "Mouse Not Found"
					updateTrayTooltip("Mouse Not Found")
					updateTrayIcon(0, false)
					broadcast(map[string]interface{}{
						"level": 0, "charging": false,
						"status":          "disconnected",
						"statusText":      "Disconnected",
						"deviceModel":     deviceModel,
						"updateAvailable": updateAvailable, "updateVersion": updateVersion,
					})

					consecutiveReadFails++

					if consecutiveReadFails >= 3 {
						if logger != nil {
							logger.Printf("[WARN] consecutiveReadFails=%d → closing device (path=%s)", consecutiveReadFails, currentHIDPath)
						}

						// --- graceful reader shutdown ---
						inputMu.Lock()
						done := readerDone // snapshot
						inputMu.Unlock()

						if device != nil {
							device.Close() // this unblocks the reader's dev.Read and lets its defer run
						}

						if done != nil {
							select {
							case <-done: // reader signalled it has cleaned up (it will nil globals itself)
							case <-time.After(800 * time.Millisecond):
								// last resort: if it didn't exit in time, clear state
								inputMu.Lock()
								inputFrames = nil
								inputDev = nil
								readerDone = nil
								inputMu.Unlock()
							}
						}

						// now clear device state
						device = nil
						hasPrevCharging = false
						selectedReportLen = 65
						selectedReportID = 0x00
						useGetOnly = false
						useInputReports = false
						consecutiveReadFails = 0
					}
				}
				<-ticker.C
				continue
			} else {
				softLinkDownCount = 0
			}

			// battery == -1 means "invalid/no data"
			if battery >= 0 {
				consecutiveReadFails = 0
				recordedUnplug = false

				if !hasPrevCharging {
					wasCharging = charging
					hasPrevCharging = true
				}

				// Detect charge completion (skip when battery==0 to avoid noise)
				if wasCharging && !charging && battery > 0 {
					// avoid recording an obviously bogus "last charged at 1–2%" blip
					if battery >= lastChargeLevel || battery >= 10 {
						lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
						lastChargeLevel = battery
						saveChargeData()
					}
				}

				// Reset flags when charging
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

				// Notifications only when we have a real level (>=0), and not charging
				if settings.NotificationsEnabled && !charging && battery >= 0 {
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
				// Show 0% correctly
				batteryText = fmt.Sprintf("%s %d%% (%s)", icon, battery, status)
				updateTrayTooltip(fmt.Sprintf("Battery: %d%%", battery))
				updateTrayIcon(battery, charging)

				// ETA only when we have rate and a sensible level
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
					"status":          "connected", // connection state
					"mode":            status,      // "Charging"/"Discharging"
					"statusText":      status,      // ← back-compat for old UI
					"level":           battery,
					"charging":        charging,
					"lastChargeTime":  lastChargeTime,
					"lastChargeLevel": lastChargeLevel,
					"deviceModel":     deviceModel,
					"timeRemaining":   timeRemaining,
					"updateAvailable": updateAvailable,
					"updateVersion":   updateVersion,
				})
			} else {
				// invalid read → force reconnect next tick
				batteryLvl = 0
				isCharging = false
				batteryText = "Connecting..."
				updateTrayTooltip("Connecting…")
				updateTrayIcon(0, false)
				broadcast(map[string]interface{}{
					"level":      0,
					"charging":   false,
					"status":     "connecting",
					"statusText": "Connecting",
				})
			}
		} else {
			batteryLvl = 0
			isCharging = false
			batteryText = "Mouse Not Found"
			updateTrayTooltip("Mouse Not Found")
			updateTrayIcon(0, false)
			broadcast(map[string]interface{}{
				"level":      0,
				"charging":   false,
				"status":     "disconnected",
				"statusText": "Disconnected",
			})
		}

		<-ticker.C
	}
}

func updateTrayTooltip(text string) {
	tip, _ := syscall.UTF16FromString(text)

	trayInvoke(func() {
		trayMu.Lock()
		defer trayMu.Unlock()

		// Clear + bounded copy into SzTip
		for i := range nid.SzTip {
			nid.SzTip[i] = 0
		}
		n := len(tip)
		if n > len(nid.SzTip) {
			n = len(nid.SzTip)
		}
		copy(nid.SzTip[:n], tip[:n])

		// 1) Focus the icon (helps some shells)
		win.Shell_NotifyIcon(win.NIM_SETFOCUS, &nid)

		// 2) Apply the new tooltip AND ask Explorer to show it now
		//    NOTE: 0x00000080 is NIF_SHOWTIP (not defined in lxn/win)
		nid.UFlags = win.NIF_TIP | 0x00000080
		win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)

		// 3) (Re)assert v4 behavior—harmless if already set
		nid.UVersion = win.NOTIFYICON_VERSION_4
		win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)

		// 4) Restore baseline flags for future updates
		nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
	})
}

/* ---------------------- */
// parseBattery tries to find the 0x83 token and interpret following bytes
// in a couple of known layouts. Returns (level, charging, ok).
func parseBattery(buf []byte) (int, bool, bool) {
	if len(buf) < 9 {
		return 0, false, false
	}

	// Accept several known tokens (some firmwares use 0x82/0x81/0x80 instead of 0x83)
	isTok := func(b byte) bool { return b == 0x83 || b == 0x82 || b == 0x81 || b == 0x80 }

	// Try fixed offsets first (fast path).
	for _, off := range []int{0, 1, 2} {
		iTok := 6 + off
		ichg := 7 + off
		ibat := 8 + off
		if iTok < len(buf) && ichg < len(buf) && ibat < len(buf) && isTok(buf[iTok]) {
			lvl := int(buf[ibat])
			chg := buf[ichg] == 1
			if lvl >= 0 && lvl <= 100 {
				return lvl, chg, true
			}
		}
	}

	// Fallback: search for token in the first 20 bytes
	maxScan := len(buf)
	if maxScan > 20 {
		maxScan = 20
	}
	for i := 0; i < maxScan; i++ {
		if !isTok(buf[i]) {
			continue
		}
		// Try [TOKEN, CHG, LVL]
		if i+2 < len(buf) {
			ch := buf[i+1]
			lv := int(buf[i+2])
			if (ch == 0 || ch == 1) && lv >= 0 && lv <= 100 {
				return lv, ch == 1, true
			}
		}
		// Try [TOKEN, LVL, CHG]
		if i+2 < len(buf) {
			lv := int(buf[i+1])
			ch := buf[i+2]
			if (ch == 0 || ch == 1) && lv >= 0 && lv <= 100 {
				return lv, ch == 1, true
			}
		}
	}

	return 0, false, false
}

func sendBatteryFeatureAnyLen(d *hid.Device, reportID byte, body []byte) error {
	sizes := []int{9, 16, 33, 65} // common FeatureReportByteLength values (incl. 1 for ReportID)
	var lastErr error
	for _, sz := range sizes {
		if sz < 1 {
			continue
		}
		buf := make([]byte, sz)
		buf[0] = reportID
		// copy as much of the body as fits
		copy(buf[1:], body)
		if _, err := d.SendFeatureReport(buf); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func getBatteryFeatureAnyLen(d *hid.Device, reportID byte) (int, bool, bool, int) {
	sizes := []int{65, 33, 16, 9} // try 65, then one smaller; skip 16/9 to stay snappy
	for _, sz := range sizes {
		buf := make([]byte, sz)
		buf[0] = reportID
		n, err := d.GetFeatureReport(buf)
		if err != nil {
			// Fast-bail if driver says "Incorrect function"
			if strings.Contains(strings.ToLower(err.Error()), "incorrect function") {
				if logger != nil {
					logger.Printf("GetFeatureReport invalid on this path (rid=0x%02x, len=%d): %v", reportID, sz, err)
				}
				return 0, false, false, 0
			}
			if logger != nil {
				logger.Printf("GetFeatureReport err (rid=0x%02x, len=%d): %v", reportID, sz, err)
			}
			continue
		}
		if n > 0 {
			if lvl, chg, ok := parseBattery(buf[:n]); ok {
				return lvl, chg, true, sz
			}
			// Not battery; don’t keep burning time here.
			return 0, false, false, 0
		}
	}
	return 0, false, false, 0
}

func getBatteryFromInputReportsQuick(d *hid.Device) (int, bool, bool) {
	ensureInputReader(d)
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if inputFrames == nil {
			return 0, false, false
		}
		select {
		case frame, ok := <-inputFrames:
			if !ok {
				inputMu.Lock()
				inputFrames = nil
				inputDev = nil
				inputMu.Unlock()
				return 0, false, false
			}
			if len(frame) > 0 {
				if lvl, chg, ok := parseBattery(frame); ok {
					return lvl, chg, true
				}
				if likelyNoMouse(frame) {
					return 0, false, false
				}
			}
		case <-time.After(40 * time.Millisecond):
		}
	}
	return 0, false, false
}

// Try to read battery from INPUT reports (interrupt IN). Optional OUTPUT poke first.
func getBatteryFromInputReports(d *hid.Device, reportID byte, tryPoke bool) (int, bool, bool) {
	ensureInputReader(d)

	if !safeForInput && tryPoke {
		// Never send OUTPUT pokes on non-vendor pages
		tryPoke = false
	}

	if tryPoke {
		bodies := [][]byte{
			{0x00, 0x02, 0x02, 0x00, 0x83},
			{0x00, 0x02, 0x02, 0x00, 0x80},
			{0x00, 0x02, 0x02, 0x00, 0x81},
			{0x00, 0x02, 0x02, 0x00, 0x84},
		}
		for _, body := range bodies {
			buf := make([]byte, 1+len(body))
			buf[0] = reportID
			copy(buf[1:], body)
			_, _ = d.Write(buf)
			time.Sleep(40 * time.Millisecond)
		}
	}

	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if inputFrames == nil {
			return 0, false, false
		}
		select {
		case frame, ok := <-inputFrames:
			if !ok {
				inputMu.Lock()
				inputFrames = nil
				inputDev = nil
				inputMu.Unlock()
				return 0, false, false
			}
			if len(frame) > 0 {
				if lvl, chg, ok := parseBattery(frame); ok {
					return lvl, chg, true
				}
				if likelyNoMouse(frame) {
					return 0, false, false
				}
			}
		case <-time.After(60 * time.Millisecond):
			// keep looping until deadline
		}
	}
	return 0, false, false
}

func sendBatteryCommandWithReportID(d *hid.Device, reportID byte) error {
	bodies := [][]byte{
		{0x00, 0x02, 0x02, 0x00, 0x83},
		{0x00, 0x02, 0x02, 0x00, 0x80},
		{0x00, 0x02, 0x02, 0x00, 0x81},
		{0x00, 0x02, 0x02, 0x00, 0x84}, // NEW: seen on some PixArt stacks
	}
	var lastErr error
	for _, body := range bodies {
		if err := sendBatteryFeatureAnyLen(d, reportID, body); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func sendBatteryCommand(d *hid.Device) error {
	for _, rid := range probeRIDs {
		if err := sendBatteryCommandWithReportID(d, rid); err == nil {
			return nil
		}
	}
	return fmt.Errorf("SendFeatureReport failed for all report IDs")
}

func tryProbeDevice(d *hid.Device) (int, bool, bool, byte) {
	for _, rid := range probeRIDs {

		// A) INPUT quick (no poke)
		if safeForInput { // ← guard
			if lvl, chg, ok := getBatteryFromInputReportsQuick(d); ok {
				selectedReportID = rid
				selectedReportLen = 65
				useGetOnly = true
				useInputReports = true

				if logger != nil {
					logger.Printf("[PROBE] mode=%s rid=0x%02x",
						map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
				}
				return lvl, chg, true, rid
			}
			// A2) INPUT with minimal OUTPUT poke
			if lvl, chg, ok := getBatteryFromInputReports(d, rid, true); ok {
				if logger != nil {
					logger.Printf("Probe OK via INPUT+OUTPUT poke (rid=0x%02x)", rid)
				}
				selectedReportID = rid
				selectedReportLen = 65
				useGetOnly = true
				useInputReports = true

				if logger != nil {
					logger.Printf("[PROBE] mode=%s rid=0x%02x",
						map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
				}
				return lvl, chg, true, rid
			}
		}

		// B) FEATURE GET-only
		if lvl, chg, ok, usedLen := getBatteryFeatureAnyLen(d, rid); ok {
			selectedReportID = rid
			selectedReportLen = usedLen
			useGetOnly = true
			useInputReports = false

			if logger != nil {
				logger.Printf("[PROBE] mode=%s rid=0x%02x",
					map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
			}
			return lvl, chg, true, rid
		}

		// C) FEATURE SET -> GET (poke then read)
		if safeForInput { // vendor page (>= 0xFF00)
			if err := sendBatteryCommandWithReportID(d, rid); err == nil {
				time.Sleep(120 * time.Millisecond)
				if lvl, chg, ok, usedLen := getBatteryFeatureAnyLen(d, rid); ok {
					selectedReportID = rid
					selectedReportLen = usedLen
					useGetOnly = false
					useInputReports = false

					if logger != nil {
						logger.Printf("[PROBE] mode=%s rid=0x%02x",
							map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], rid)
					}

					return lvl, chg, true, rid
				}
			}
		}
	}
	return 0, false, false, 0
}

/* ---------------------- */

func reconnect() {
	if device != nil {
		return
	}

	// 1) Collect candidates from all known Glorious VIDs
	var candidates []hid.DeviceInfo
	seen := make(map[string]bool)
	for _, vid := range gloriousVendorIDs {
		hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
			// Skip obviously unrelated devices
			lp := strings.ToLower(info.ProductStr)
			if info.UsagePage == 0x01 && info.Usage == 0x02 {
				return nil
			}
			if strings.Contains(lp, "gmmk") ||
				strings.Contains(lp, "keyboard") ||
				strings.Contains(lp, "headset") ||
				strings.Contains(lp, "audio") {
				return nil // skip
			}

			// Also skip KBD interface paths
			if strings.Contains(strings.ToLower(info.Path), `\kbd`) {
				return nil
			}

			if !seen[info.Path] {
				candidates = append(candidates, *info)
				seen[info.Path] = true
			}
			return nil
		})
	}

	// 2) Fallback: broaden search (some firmwares ship with unexpected VID/strings)
	// We only add devices that *might* be Glorious: product/manufacturer mentions,
	// known PIDs, or vendor-defined usage pages.
	if len(candidates) == 0 {
		hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
			if seen[info.Path] {
				return nil
			}

			// Skip obviously unrelated devices
			lp := strings.ToLower(info.ProductStr)
			if info.UsagePage == 0x01 && info.Usage == 0x02 {
				return nil
			}
			if strings.Contains(lp, "gmmk") ||
				strings.Contains(lp, "keyboard") ||
				strings.Contains(lp, "headset") ||
				strings.Contains(lp, "audio") {
				return nil // skip
			}

			// Also skip KBD interface paths
			if strings.Contains(strings.ToLower(info.Path), `\kbd`) {
				return nil
			}

			lowProd := strings.ToLower(info.ProductStr)
			looksGlorious := strings.Contains(lowProd, "glorious") ||
				strings.Contains(lowProd, "model o") ||
				strings.Contains(lowProd, "model d") ||
				strings.Contains(lowProd, "model i")
			isVendorPage := info.UsagePage >= 0xFF00
			_, pidKnown := deviceNames[info.ProductID]
			if looksGlorious || isVendorPage || pidKnown {
				candidates = append(candidates, *info)
				seen[info.Path] = true
			}
			return nil
		})
	}

	// Prioritize likely battery/dongle interfaces
	// After building candidates, stable-stable prioritize the exact path that matched before
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		// exact best: UsagePage vendor-defined
		aVnd := (a.UsagePage >= 0xFF00)
		bVnd := (b.UsagePage >= 0xFF00)
		if aVnd != bVnd {
			return aVnd && !bVnd
		}

		// then any vendor page over generic
		if (a.UsagePage >= 0xFF00) != (b.UsagePage >= 0xFF00) {
			return a.UsagePage >= 0xFF00
		}
		// then non-zero usage (collections)
		if (a.Usage != 0) != (b.Usage != 0) {
			return a.Usage != 0
		}
		// then lower interface number
		if a.InterfaceNbr != b.InterfaceNbr {
			return a.InterfaceNbr < b.InterfaceNbr
		}
		return false
	})

	// If we have a cached path, move it to the front if present
	if cachedProfile != nil {
		for i := range candidates {
			if candidates[i].Path == cachedProfile.Path {
				if i != 0 {
					c := candidates[i]
					copy(candidates[1:i+1], candidates[0:i])
					candidates[0] = c
				}
				break
			}
		}
	}

	// Try each interface until one responds to the battery probe
	for _, ci := range candidates {
		d, err := hid.OpenPath(ci.Path)
		if err != nil {
			continue
		}

		// Set this *before* probing so tryProbeDevice() knows if we're on vendor page.
		prevSafe := safeForInput
		safeForInput = (ci.UsagePage >= 0xFF00)

		if logger != nil {
			logger.Printf("[RECONNECT] trying path=%s usagePage=0x%04x iface=%d", ci.Path, ci.UsagePage, ci.InterfaceNbr)
		}

		// Fast-validate with cached params if this path matches
		if cachedProfile != nil && ci.Path == cachedProfile.Path {
			selectedReportID = cachedProfile.ReportID
			selectedReportLen = cachedProfile.ReportLen
			useGetOnly = cachedProfile.UseGetOnly
			useInputReports = cachedProfile.UseInputReports

			// Single quick read to confirm
			if lvl, chg, ok := quickValidate(d); ok {
				device = d
				deviceModel = pickModel(ci)
				currentHIDPath = ci.Path
				safeForInput = (ci.UsagePage >= 0xFF00)
				logConn(ci.Path, "CACHED")
				finishConnect(ci.Path, lvl, chg)
				return
			}

			// fall through to full probe if quick check failed
		}

		lvl, chg, ok, rid := tryProbeDevice(d)
		if ok {
			device = d
			selectedReportID = rid
			deviceModel = pickModel(ci)
			currentHIDPath = ci.Path
			safeForInput = (ci.UsagePage >= 0xFF00)
			logConn(ci.Path, "PROBED")
			// persist profile for this exact path
			saveConnProfile(DeviceProfile{
				Path:            ci.Path, // <- the path you actually opened
				ReportID:        selectedReportID,
				ReportLen:       selectedReportLen,
				UseGetOnly:      useGetOnly,      // <- preserve actual mode
				UseInputReports: useInputReports, // <- preserve actual mode
			})

			batteryLvl = lvl
			isCharging = chg
			status := "Discharging"
			icon := "🔋"
			if chg {
				status = "Charging"
				icon = "⚡"
			}
			batteryText = fmt.Sprintf("%s %d%% (%s)", icon, lvl, status)
			updateTrayTooltip(fmt.Sprintf("Battery: %d%%", lvl))
			updateTrayIcon(lvl, chg)
			return
		}
		currentHIDPath = ci.Path
		safeForInput = prevSafe
		d.Close()

	}
}

func quickValidate(d *hid.Device) (int, bool, bool) {
	// If cached mode was INPUT, try a single short read without poke
	if useInputReports && safeForInput {
		ensureInputReader(d)
		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			if inputFrames == nil {
				break
			}
			select {
			case frame, ok := <-inputFrames:
				if !ok {
					inputMu.Lock()
					inputFrames = nil
					inputDev = nil
					inputMu.Unlock()
					return 0, false, false
				}
				if len(frame) > 0 {
					if lvl, chg, ok := parseBattery(frame); ok {
						return lvl, chg, true
					}
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
		return 0, false, false
	}

	// FEATURE quick read: 65 only, one shot
	buf := make([]byte, 65)
	buf[0] = selectedReportID
	if n, err := d.GetFeatureReport(buf); err == nil && n > 0 {
		if lvl, chg, ok := parseBattery(buf[:n]); ok {
			return lvl, chg, true
		}
	}
	return 0, false, false
}

func likelyNoMouse(buf []byte) bool {
	// Very short “nothing here” frames
	if len(buf) <= 5 {
		return true
	}

	// If first up to 16 bytes are all 0x00/0xFF → likely no link
	max := len(buf)
	if max > 16 {
		max = 16
	}
	nonTrivial := 0
	for i := 0; i < max; i++ {
		if buf[i] != 0x00 && buf[i] != 0xFF {
			nonTrivial++
		}
	}
	if nonTrivial == 0 {
		return true
	}

	// Common idle: reportID + ~8 zeros
	if len(buf) >= 9 {
		zeros := 0
		for i := 1; i < 9; i++ {
			if buf[i] == 0x00 {
				zeros++
			}
		}
		if zeros >= 7 {
			return true
		}
	}
	return false
}

func testBattery(d *hid.Device) (int, bool) {
	lvl, chg, ok, _ := tryProbeDevice(d) // ignore report ID here
	if ok {
		return lvl, chg
	}
	return 0, false
}

func readBattery() (int, bool) {
	if device == nil {
		linkDown = true
		if logger != nil {
			logger.Printf("[LINK] down on path=%s", currentHIDPath)
		}
		return -1, false
	}

	// Fast path: input reports
	if useInputReports && safeForInput {
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, false); ok {
			linkDown = false
			recordedUnplug = false

			if logger != nil {
				logger.Printf("[READ] linkDown=%v lvl=%d chg=%v (path=%s, rid=0x%02x, mode=%s len=%d)",
					linkDown, lvl, chg, currentHIDPath, selectedReportID,
					map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], selectedReportLen)
			}

			return lvl, chg
		}
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, true); ok {
			linkDown = false
			recordedUnplug = false

			if logger != nil {
				logger.Printf("[READ] linkDown=%v lvl=%d chg=%v (path=%s, rid=0x%02x, mode=%s len=%d)",
					linkDown, lvl, chg, currentHIDPath, selectedReportID,
					map[bool]string{true: "INPUT", false: "FEATURE"}[useInputReports], selectedReportLen)
			}

			return lvl, chg
		}
		linkDown = true
		if logger != nil {
			logger.Printf("[LINK] down on path=%s", currentHIDPath)
		}
		return -1, false
	}

	// FEATURE path (for 0x258A etc.)
	if !useGetOnly {
		if err := sendBatteryCommandWithReportID(device, selectedReportID); err != nil {
			if logger != nil {
				logger.Printf("SendFeatureReport(read) failed (rid=0x%02x): %v (switching to GET-only for this session)", selectedReportID, err)
			}
			useGetOnly = true
		} else {
			time.Sleep(150 * time.Millisecond)
		}
	}

	rl := selectedReportLen
	if rl < 2 || rl > 65 {
		rl = 65
	}

	buf := make([]byte, rl)
	buf[0] = selectedReportID
	n, err := device.GetFeatureReport(buf)
	if err == nil && n > 0 {
		if lvl, chg, ok := parseBattery(buf[:n]); ok {
			linkDown = false
			recordedUnplug = false
			return lvl, chg
		}
		if likelyNoMouse(buf[:n]) {
			linkDown = true
			if logger != nil {
				logger.Printf("[LINK] down on path=%s", currentHIDPath)
			}
			return -1, false
		}
	}

	// Retry with 65 bytes
	if rl != 65 {
		big := make([]byte, 65)
		big[0] = selectedReportID
		if n2, err2 := device.GetFeatureReport(big); err2 == nil && n2 > 0 {
			if lvl, chg, ok := parseBattery(big[:n2]); ok {
				selectedReportLen = 65
				linkDown = false
				recordedUnplug = false
				return lvl, chg
			}
			if likelyNoMouse(big[:n2]) {
				linkDown = true
				if logger != nil {
					logger.Printf("[LINK] down on path=%s", currentHIDPath)
				}
				return -1, false
			}
		}
	}

	// Last-ditch: try INPUT in case the device actually talks there
	if safeForInput {
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, false); ok {
			useInputReports = true
			linkDown = false
			recordedUnplug = false
			p := currentHIDPath
			if p == "" && cachedProfile != nil {
				p = cachedProfile.Path
			}
			saveConnProfile(DeviceProfile{
				Path:            p,
				ReportID:        selectedReportID,
				ReportLen:       selectedReportLen,
				UseGetOnly:      true,
				UseInputReports: true,
			})
			return lvl, chg
		}
		if lvl, chg, ok := getBatteryFromInputReports(device, selectedReportID, true); ok {
			useInputReports = true
			linkDown = false
			recordedUnplug = false
			return lvl, chg
		}
	}

	linkDown = true
	if logger != nil {
		logger.Printf("[LINK] down on path=%s", currentHIDPath)
	}
	return -1, false
}

func createBatteryIcon(level int, charging bool, frame int) win.HICON {
	defer safeDefer("createBatteryIcon")

	getSystemMetrics := user32.NewProc("GetSystemMetrics")
	smCxIcon, _, _ := getSystemMetrics.Call(uintptr(11)) // SM_CXICON
	smCyIcon, _, _ := getSystemMetrics.Call(uintptr(12)) // SM_CYICON

	width := int32(smCxIcon) * 2
	height := int32(smCyIcon) * 2
	if width < 64 {
		width = 64
	}
	if height < 64 {
		height = 64
	}

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
		Height:      -height, // top-down is fine
		Planes:      1,
		BitCount:    32,
		Compression: 0, // BI_RGB instead of BI_BITFIELDS
		// leave masks as 0 when BI_RGB
		RedMask:   0,
		GreenMask: 0,
		BlueMask:  0,
		AlphaMask: 0,
	}

	hdc := win.GetDC(0)
	if hdc == 0 {
		if logger != nil {
			logger.Printf("[ICON] GetDC failed")
		}
		return nid.HIcon // keep old icon
	}
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
		if logger != nil {
			logger.Printf("[ICON] CreateDIBSection failed")
		}
		return nid.HIcon
	}

	// Use int for length to satisfy unsafe.Slice
	total := int(width) * int(height)
	pixelSlice := unsafe.Slice((*uint32)(pBits), total)

	inBounds := func(x, y int32) bool { return x >= 0 && y >= 0 && x < width && y < height }
	set := func(x, y int32, c uint32) {
		if inBounds(x, y) {
			pixelSlice[int(y)*int(width)+int(x)] = c
		}
	}

	for i := range pixelSlice {
		pixelSlice[i] = 0x00000000
	}

	var fillColor uint32
	if charging {
		fillColor = 0xFF0078D4
	} else if level >= 50 {
		fillColor = 0xFF107C10
	} else if level >= 20 {
		fillColor = 0xFFF7630C
	} else {
		fillColor = 0xFFC42B1C
	}
	black := uint32(0xFF000000)
	white := uint32(0xFFFFFFFF)

	scale := float32(width) / 64.0
	if scale <= 0 {
		scale = 1
	}

	bodyLeft := int32(float32(10) * scale)
	bodyTop := int32(float32(20) * scale)
	bodyRight := int32(float32(50) * scale)
	bodyBottom := int32(float32(44) * scale)
	if bodyRight <= bodyLeft+2 {
		bodyRight = bodyLeft + 2
	}
	if bodyBottom <= bodyTop+2 {
		bodyBottom = bodyTop + 2
	}
	if bodyRight >= width {
		bodyRight = width - 1
	}
	if bodyBottom >= height {
		bodyBottom = height - 1
	}

	for y := bodyTop; y <= bodyBottom; y++ {
		for x := bodyLeft; x <= bodyRight; x++ {
			if y == bodyTop || y == bodyBottom || x == bodyLeft || x == bodyRight {
				set(x, y, black)
			} else {
				set(x, y, white)
			}
		}
	}

	tipLeft := bodyRight + 1
	tipRight := int32(float32(56) * scale)
	tipTop := int32(float32(27) * scale)
	tipBottom := int32(float32(37) * scale)
	if tipRight < tipLeft {
		tipRight = tipLeft
	}
	if tipBottom < tipTop {
		tipBottom = tipTop
	}
	if tipRight >= width {
		tipRight = width - 1
	}
	if tipBottom >= height {
		tipBottom = height - 1
	}
	for y := tipTop; y <= tipBottom; y++ {
		for x := tipLeft; x <= tipRight; x++ {
			set(x, y, black)
		}
	}

	if level > 0 {
		displayLevel := level
		if charging {
			displayLevel = level + (frame * 10)
			if displayLevel > 100 {
				displayLevel = 100
			}
		}
		maxFillPixels := (bodyRight - (bodyLeft + 1))
		if maxFillPixels < 0 {
			maxFillPixels = 0
		}
		fw := int32(float32(38) * scale * float32(displayLevel) / 100.0)
		if fw < 0 {
			fw = 0
		}
		if fw > maxFillPixels {
			fw = maxFillPixels
		}
		for y := bodyTop + 1; y < bodyBottom; y++ {
			for x := bodyLeft + 1; x < bodyLeft+1+fw; x++ {
				set(x, y, fillColor)
			}
		}
	}

	hMask := win.CreateBitmap(width, height, 1, 1, nil) // or unsafe.Pointer(nil)
	if hMask == 0 {
		if logger != nil {
			logger.Printf("[ICON] CreateBitmap(mask) failed")
		}
		win.DeleteObject(win.HGDIOBJ(hBitmap))
		return nid.HIcon
	}

	var iconInfo win.ICONINFO
	iconInfo.FIcon = 1
	iconInfo.HbmColor = win.HBITMAP(hBitmap)
	iconInfo.HbmMask = hMask // ← give a real mask

	hIcon := win.CreateIconIndirect(&iconInfo)
	if hIcon == 0 {
		if logger != nil {
			logger.Printf("[ICON] CreateIconIndirect failed")
		}
		win.DeleteObject(win.HGDIOBJ(hBitmap))
		win.DeleteObject(win.HGDIOBJ(hMask))
		return nid.HIcon
	}

	// We no longer need the source bitmaps after the HICON is created.
	win.DeleteObject(win.HGDIOBJ(hBitmap))
	win.DeleteObject(win.HGDIOBJ(hMask))

	return hIcon
}

func updateTrayIcon(level int, charging bool) {
	newIcon := createBatteryIcon(level, charging, animationFrame)
	if newIcon == 0 {
		return
	}
	trayInvoke(func() {
		trayMu.Lock()
		oldIcon := nid.HIcon
		nid.HIcon = newIcon

		// IMPORTANT: include NIF_TIP and SHOWTIP so we don't stomp tooltip updates.
		// 0x00000080 == NIF_SHOWTIP (not defined in lxn/win)
		nid.UFlags = win.NIF_ICON | win.NIF_TIP | 0x00000080
		win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)

		// Keep baseline flags consistent
		nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
		trayMu.Unlock()

		if oldIcon != 0 && oldIcon != newIcon {
			select {
			case iconReap <- oldIcon:
			default:
			}
		}
	})
}

func animateChargingIcon() {
	defer safeDefer("animateChargingIcon")
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("animateChargingIcon recovered: %v", r)
			}
		}
	}()
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
	fileMu.Lock()
	defer fileMu.Unlock()
	_ = os.WriteFile(dataFile, data, 0644)
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
	fileMu.Lock()
	_ = os.WriteFile(settingsFile, data, 0644)
	fileMu.Unlock()

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
		if newSettings.RefreshInterval < 1 {
			newSettings.RefreshInterval = 5
		}
		settings = newSettings
		saveSettings()
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}
}

func loadConnProfile() {
	b, err := os.ReadFile(cacheFile)
	if err != nil || len(b) == 0 {
		return
	}
	var p DeviceProfile
	if json.Unmarshal(b, &p) == nil && p.Path != "" {
		cachedProfile = &p
	}
}
func saveConnProfile(p DeviceProfile) {
	cachedProfile = &p
	b := mustJSON(p)
	fileMu.Lock()
	_ = os.WriteFile(cacheFile, b, 0644)
	fileMu.Unlock()
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
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

// ---------- Startup shortcut helpers (Startup folder .lnk) ----------

func startupShortcutPath(appName string) string {
	// %APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\<app>.lnk
	return filepath.Join(
		os.Getenv("APPDATA"),
		`Microsoft\Windows\Start Menu\Programs\Startup`,
		appName+".lnk",
	)
}

func createStartupShortcut(appName, exePath, args string) error {
	// Ensure the Startup folder exists
	startupDir := filepath.Dir(startupShortcutPath(appName))
	if err := os.MkdirAll(startupDir, 0755); err != nil {
		return err
	}

	linkPath := startupShortcutPath(appName)

	// Initialize COM (STA)
	if err := ole.CoInitialize(0); err != nil {
		return fmt.Errorf("CoInitialize failed: %v", err)
	}
	defer ole.CoUninitialize()

	// Use WScript.Shell (automation-friendly) to create the .lnk
	shellObj, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("CreateObject(WScript.Shell) failed: %v", err)
	}
	defer shellObj.Release()

	shellDisp, err := shellObj.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("QueryInterface IDispatch failed: %v", err)
	}
	defer shellDisp.Release()

	// shortcut = WScript.Shell.CreateShortcut(linkPath)
	scV, err := oleutil.CallMethod(shellDisp, "CreateShortcut", linkPath)
	if err != nil {
		return fmt.Errorf("CreateShortcut failed: %v", err)
	}
	sc := scV.ToIDispatch()
	defer sc.Release()

	// Set properties
	if _, err = oleutil.PutProperty(sc, "TargetPath", exePath); err != nil {
		return fmt.Errorf("Set TargetPath failed: %v", err)
	}
	if strings.TrimSpace(args) != "" {
		if _, err = oleutil.PutProperty(sc, "Arguments", args); err != nil {
			return fmt.Errorf("Set Arguments failed: %v", err)
		}
	}
	_, _ = oleutil.PutProperty(sc, "Description", appName)
	_, _ = oleutil.PutProperty(sc, "IconLocation", exePath) // optional
	// 7 = SW_SHOWMINNOACTIVE, 1 = SW_NORMAL. Keep normal; you already handle minimize via your own logic.
	_, _ = oleutil.PutProperty(sc, "WindowStyle", 1)

	// Save the shortcut
	if _, err = oleutil.CallMethod(sc, "Save"); err != nil {
		return fmt.Errorf("Shortcut Save failed: %v", err)
	}
	return nil
}

func removeStartupShortcut(appName string) error {
	linkPath := startupShortcutPath(appName)
	if _, err := os.Stat(linkPath); err == nil {
		return os.Remove(linkPath)
	}
	return nil
}

// ---------- Public API used by your settings logic ----------

func enableStartup() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	// If you want the app to start minimized when launched from Startup:
	args := ""
	if settings.StartMinimized {
		args = "--minimized"
	}
	_ = createStartupShortcut("GloriousBatteryMonitor", exePath, args)
}

func disableStartup() {
	_ = removeStartupShortcut("GloriousBatteryMonitor")
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
	infoTitle, _ := syscall.UTF16FromString(title)
	infoText, _ := syscall.UTF16FromString(message)

	trayInvoke(func() {
		trayMu.Lock()
		nid.UFlags = win.NIF_INFO
		if critical {
			nid.DwInfoFlags = win.NIIF_WARNING
		} else {
			nid.DwInfoFlags = win.NIIF_INFO
		}
		copy(nid.SzInfoTitle[:], infoTitle)
		copy(nid.SzInfo[:], infoText)
		win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)

		// reset flags
		nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
		win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
		trayMu.Unlock()
	})
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

	resp, err := http.Get("https://api.github.com/repos/Rodrigo-200/GloriousBatteryMonitor-Go/releases/latest")
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
			if asset.Name == "GloriousBatteryMonitor-Go.exe" {
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

	// Rename old exe to .old
	oldFile := exePath + ".old"
	os.Remove(oldFile) // Remove any existing .old file
	if err := os.Rename(exePath, oldFile); err != nil {
		return err
	}

	// Rename new exe to current exe
	if err := os.Rename(tempFile, exePath); err != nil {
		// Restore old file if rename fails
		os.Rename(oldFile, exePath)
		return err
	}

	// Restart the application
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExecute := shell32.NewProc("ShellExecuteW")
	exePathW, _ := syscall.UTF16PtrFromString(exePath)
	verb, _ := syscall.UTF16PtrFromString("open")
	shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exePathW)), 0, 0, 1)

	// Exit current process
	terminateProcess := kernel32.NewProc("TerminateProcess")
	getCurrentProcess := kernel32.NewProc("GetCurrentProcess")
	handle, _, _ := getCurrentProcess.Call()
	terminateProcess.Call(handle, 0)

	return nil
}

func pickModel(ci hid.DeviceInfo) string {
	if ci.ProductStr != "" {
		return ci.ProductStr
	}
	if name, ok := deviceNames[ci.ProductID]; ok {
		return name
	}
	return "Unknown"
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

func finishConnect(path string, lvl int, chg bool) {
	recordedUnplug = false
	saveConnProfile(DeviceProfile{
		Path:            path,
		ReportID:        selectedReportID,
		ReportLen:       selectedReportLen,
		UseGetOnly:      useGetOnly,
		UseInputReports: useInputReports,
	})
	batteryLvl = lvl
	isCharging = chg
	status := "Discharging"
	icon := "🔋"
	if chg {
		status, icon = "Charging", "⚡"
	}
	batteryText = fmt.Sprintf("%s %d%% (%s)", icon, lvl, status)
	updateTrayTooltip(fmt.Sprintf("Battery: %d%%", lvl))
	updateTrayIcon(lvl, chg)
}

func setupLogging() {
	// Ensure parent dir exists
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		// fall back silently if needed
	}

	// Open + truncate so each run starts with a clean log
	logFileHandle, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		// If truncate fails for some reason, try append as a fallback
		logFileHandle, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			// Last resort: keep default logger (no file)
			log.Printf("Failed to open log file: %v", err)
			return
		}
	}

	// Send the stdlib logger output to the file too (so all log.Printf end up here)
	log.SetOutput(logFileHandle)
	// Optional: set flags to match your custom logger
	log.SetFlags(log.LstdFlags)

	// Your dedicated logger that also writes to the same file
	logger = log.New(logFileHandle, "", log.LstdFlags)

	logger.Printf("=== Glorious Battery Monitor v%s Started ===", currentVersion)
	logger.Printf("Log file location: %s", logFile)

	scanAllDevices()
}

func scanAllDevices() {
	logger.Printf("=== Scanning for HID devices ===")
	for _, vid := range gloriousVendorIDs {
		logger.Printf("Scanning for Glorious VID: 0x%04x", vid)
		found := 0
		hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
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
		if count < 15 {
			looksGlorious := strings.Contains(strings.ToLower(info.ProductStr), "glorious")
			isVendorPage := info.UsagePage >= 0xFF00
			_, pidKnown := deviceNames[info.ProductID]
			if looksGlorious || isVendorPage || pidKnown {
				logger.Printf("Fallback candidate - VID: 0x%04x, PID: 0x%04x, Prod: %q, UsagePage: 0x%04x, If#: %d, Path: %s",
					info.VendorID, info.ProductID, info.ProductStr, info.UsagePage, info.InterfaceNbr, info.Path)
				count++
			}
		}
		return nil
	})
}
