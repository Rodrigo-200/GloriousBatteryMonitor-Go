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
	"strings"
	"sync"
	"sync/atomic"
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
	WM_APP_TRAY_PING = WM_APP + 20

	ID_SHOW   = 1001
	ID_QUIT   = 1002
	ID_UPDATE = 1003
	ID_RESCAN = 1004

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
	WM_DEVICECHANGE  = 0x0219
)

type ChargeData struct {
	LastChargeTime  string  `json:"lastChargeTime"`
	LastChargeLevel int     `json:"lastChargeLevel"`
	DischargeRate   float64 `json:"dischargeRate"`
	ChargeRate      float64 `json:"chargeRate"`
	Timestamp       string  `json:"timestamp"`
}

type Settings struct {
	StartWithWindows     bool `json:"startWithWindows"`
	StartMinimized       bool `json:"startMinimized"`
	RefreshInterval      int  `json:"refreshInterval"` // in seconds
	NotificationsEnabled bool `json:"notificationsEnabled"`
	NonIntrusiveMode     bool `json:"nonIntrusiveMode"`
	// When true, the app will prefer using the helper worker to open and
	// hold a persistent session for wireless dongles or vendor-specific
	// interfaces. This avoids local opens which can produce stale/0% reads
	// on flaky receivers.
	PreferWorkerForWireless  bool `json:"preferWorkerForWireless"`
	LowBatteryThreshold      int  `json:"lowBatteryThreshold"`      // percentage
	CriticalBatteryThreshold int  `json:"criticalBatteryThreshold"` // percentage
}

const currentVersion = "2.3.4"

var (
	device           *hid.Device
	deviceModel      string = "Unknown"
	hwnd             win.HWND
	webviewHwnd      win.HWND
	nid              win.NOTIFYICONDATA
	batteryText      string = "Connecting..."
	batteryLvl       int
	isCharging       bool
	wasCharging      bool
	hasPrevCharging  bool
	lastChargeTime   string       = "Never"
	lastChargeLevel  int          = 0
	user32                        = syscall.NewLazyDLL("user32.dll")
	appendMenuW                   = user32.NewProc("AppendMenuW")
	showWindow                    = user32.NewProc("ShowWindow")
	clients                       = make(map[chan string]bool)
	clientsMu        sync.RWMutex // ← add
	w                webview2.WebView
	serverPort       string = "8765"
	dataDir          string
	dataFile         string
	settingsFile     string
	logFile          string
	logger           *log.Logger
	settings         Settings
	notifiedLow      bool
	notifiedCritical bool
	notifiedFull     bool
	lastBatteryLevel int = -1
	lastBatteryTime  time.Time
	dischargeRate    float64 = 0
	lastChargeLevel2 int     = -1
	lastChargeTime2  time.Time
	// lastKnown* holds the most recent valid reading so the UI can
	// display a sensible 'last known' value when the device disconnects.
	lastKnownLevel    int = -1
	lastKnownCharging bool
	lastKnownMu       sync.Mutex
	// When true we should display the last-known value instead of
	// an immediate "Mouse Not Found"/0% message.
	showLastKnown        bool
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
	probeRIDs            = []byte{0x04, 0x03, 0x02, 0x01, 0x00}
	useInputReports      bool
	inputFrames          chan []byte
	cacheFile            string
	cachedProfiles       []DeviceProfile
	softLinkDownCount    int
	currentHIDPath       string
	fileMu               sync.Mutex
	safeForInput         bool
	inputDev             *hid.Device
	inputMu              sync.Mutex
	recordedUnplug       bool
	// dropConfirm prevents multiple overlapping confirmation routines
	// that attempt to validate a sudden large drop in reported level.
	dropConfirmMu     sync.Mutex
	dropConfirmActive bool
	trayMu            sync.Mutex
	trayOps           = make(chan func(), 64)
	iconReap          = make(chan win.HICON, 64)
	// iconCache stores generated HICONs keyed by "lvl:charging:frame" so
	// we can reuse them instead of regenerating on every update which
	// reduces GDI load and helps keep the tray thread responsive.
	iconCache   = make(map[string]win.HICON)
	iconCacheMu sync.Mutex
	// cachedDisconnectedIcon speeds up updates when the device disconnects
	// so we don't regenerate a full DIB-backed icon on every disconnect
	// event (which can block the tray message loop). Created on demand
	// on the tray thread and reused afterwards.
	cachedDisconnectedIcon win.HICON
	cachedIconMu           sync.Mutex
	readerDone             chan struct{}
	taskbarCreated         = win.RegisterWindowMessage(syscall.StringToUTF16Ptr("TaskbarCreated"))
	// readingUntil is used to indicate a short verification window after a
	// connection where single zero reads should be treated as suspicious.
	readingUntil time.Time
	readingMu    sync.Mutex
	// tray ping/pong used by a watchdog to detect an unresponsive tray
	lastTrayPing   time.Time
	lastTrayPong   time.Time
	lastTrayPongMu sync.Mutex
	lastTrayPingMu sync.Mutex
	// Watchdog counter for consecutive missing pongs
	watchdogNoPongCount int

	// Device-change debounce / fresh-probe control. Use atomics so the
	// tray thread can record device-change events without acquiring a
	// mutex that may be held by background reconnect/driver ops.
	lastDevChangeUnix      int64 // unix nano timestamp
	devChangeScheduledInt  int32 // 0 = not scheduled, 1 = scheduled
	forceFreshProbeOnceInt int32 // atomic flag consumed by reconnect()
	// lastGoodReadUnix is the unix-nano timestamp of the most recent
	// successful battery read. Use it to bias quick probe acceptance
	// decisions (prefer a recent good reading over a single low probe).
	lastGoodReadUnix int64
	// forceLiveUntilInt64 causes the server to prefer presenting a
	// recent live reading for a short window after a successful
	// connect/quick-probe. Stored as unix-nano timestamp for atomic ops.
	forceLiveUntilInt64 int64
	// When true we should never perform local opens for wireless/receiver
	// interfaces and should prefer the helper worker exclusively. This is
	// controlled via env GLORIOUS_FORCE_WORKER=1 for quick testing.
	forceWorkerMode bool
)

func safeDefer(where string) {
	if r := recover(); r != nil {
		if logger != nil {
			logger.Printf("[RECOVER] %s: %v", where, r)
		}
	}
}

// absInt returns the absolute value of an int.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
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
	// If started as a helper worker process, run workerMain and exit
	for _, a := range os.Args[1:] {
		if a == "--hid-worker" {
			workerMain()
			return
		}
	}

	// Set up data file paths
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = "."
	}
	dataDir = filepath.Join(appData, "GloriousBatteryMonitor")
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

	// Allow quick forced-worker testing via env var so users can avoid
	// local opens which sometimes cause link flapping/crashes. Set
	// GLORIOUS_FORCE_WORKER=1 to enable.
	if os.Getenv("GLORIOUS_FORCE_WORKER") == "1" {
		forceWorkerMode = true
		if logger != nil {
			logger.Printf("[STARTUP] forceWorkerMode enabled via GLORIOUS_FORCE_WORKER")
		}
	}

	// If we have stored last-charge info, show it immediately while the
	// app starts up and probes devices so the UI is useful before a
	// fresh read is performed.
	if lastChargeLevel > 0 {
		lastKnownMu.Lock()
		lastKnownLevel = lastChargeLevel
		lastKnownCharging = false
		showLastKnown = true
		lastKnownMu.Unlock()
	}

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

	// Headless shortcut (useful for debugging crashes in the UI):
	// set GLORIOUS_NO_UI=1 to skip WebView2 and tray initialization.
	if os.Getenv("GLORIOUS_NO_UI") == "1" {
		if logger != nil {
			logger.Printf("[STARTUP] GLORIOUS_NO_UI set; running headless (server + updater only)")
		}
		go startWebServer()
		if os.Getenv("GLORIOUS_NO_HID") == "1" {
			if logger != nil {
				logger.Printf("[STARTUP] GLORIOUS_NO_HID set; skipping HID/init/updateBattery")
			}
		} else {
			go updateBattery()
		}
		// Block main goroutine so process stays alive for diagnosis
		select {}
	}

	stopAnimation = make(chan bool)
	go startWebServer()
	if logger != nil {
		logger.Printf("[STARTUP] started webserver goroutine")
	}
	go startTray()
	if logger != nil {
		logger.Printf("[STARTUP] started tray goroutine")
	}
	go updateBattery()
	if logger != nil {
		logger.Printf("[STARTUP] started updateBattery goroutine")
	}
	go animateChargingIcon()
	if logger != nil {
		logger.Printf("[STARTUP] started animateChargingIcon goroutine")
	}

	// Start the helper probe worker early so it's ready for reconnect
	// probes and fallbacks. Do this asynchronously so the worker can
	// initialize while other startup tasks complete.
	go func() {
		if err := StartProbeWorker(); err != nil {
			if logger != nil {
				logger.Printf("[WORKER] background StartProbeWorker failed: %v", err)
			}
			return
		}
		if logger != nil {
			logger.Printf("[WORKER] background helper started")
		}
	}()

	// If we don't already have a cached profile, perform a one-shot quick
	// worker-based probe across likely vendor devices so that first-time
	// runs discover the correct interface quickly without hammering many
	// slow local probes. quickRefreshOnDeviceChange contains the same
	// probe logic but is optimized for device-change events — reuse it
	// here as a fast startup heuristic.
	if len(cachedProfiles) == 0 {
		if logger != nil {
			logger.Printf("[STARTUP] no cached profile — running quick startup probe")
		}
		go quickRefreshOnDeviceChange()
	}

	time.Sleep(500 * time.Millisecond)

	// Memory optimization: Set WebView2 browser arguments via environment variable
	// Reduces memory usage by ~40-50MB with minimal performance impact
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--disable-gpu --disable-software-rasterizer --disable-extensions --disable-background-networking --disk-cache-size=1 --media-cache-size=1 --disable-features=AudioServiceOutOfProcess")

	if logger != nil {
		logger.Printf("[STARTUP] creating WebView2 instance")
	}
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("[STARTUP] panic during WebView2 creation: %v\n%s", r, debug.Stack())
			}
		}
	}()
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
		if logger != nil {
			logger.Printf("[STARTUP] WebView2 returned nil")
		}
		return
	}
	if logger != nil {
		logger.Printf("[STARTUP] WebView2 created successfully")
	}
	defer w.Destroy()

	webviewHwnd = win.HWND(w.Window())
	if logger != nil {
		logger.Printf("[STARTUP] webview HWND = 0x%X", uintptr(webviewHwnd))
	}

	// Load and set window icon
	hInst := win.GetModuleHandle(nil)
	hIcon := win.LoadIcon(hInst, win.MAKEINTRESOURCE(1))
	if hIcon != 0 {
		if logger != nil {
			logger.Printf("[STARTUP] loaded icon: 0x%X", uintptr(hIcon))
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					if logger != nil {
						logger.Printf("[STARTUP] panic while setting window icon: %v\n%s", r, debug.Stack())
					}
				}
			}()
			win.SendMessage(webviewHwnd, win.WM_SETICON, 0, uintptr(hIcon)) // Small icon
		}()
	}
	// Hook the webview window procedure so we can intercept the close and
	// forward to the original proc. This is required to keep the webview
	// window responsive and avoid native crashes when integrating with the tray.
	if logger != nil {
		logger.Printf("[STARTUP] hooking webview window procedure and navigating to UI")
	}
	// Save old WNDPROC and store it in GWLP_USERDATA for retrieval from our proc.
	oldProc := win.SetWindowLongPtr(webviewHwnd, win.GWLP_WNDPROC, syscall.NewCallback(webviewWndProc))
	win.SetWindowLongPtr(webviewHwnd, win.GWLP_USERDATA, oldProc)
	// Navigate to our embedded HTTP server UI and run the webview loop.
	url := fmt.Sprintf("http://localhost:%s", serverPort)
	if logger != nil {
		logger.Printf("[STARTUP] webview navigating to %s", url)
	}
	w.Navigate(url)
	// Start minimized if setting is enabled
	if settings.StartMinimized {
		showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_HIDE))
	}
	if logger != nil {
		logger.Printf("[STARTUP] entering webview run loop")
	}
	w.Run()
	if logger != nil {
		logger.Printf("[STARTUP] webview run loop exited")
	}
}

// previous setup/adopt handler removed during rollback

func serveHTML(w http.ResponseWriter, r *http.Request) {
	data, _ := content.ReadFile("ui.html")
	w.Header().Set("Content-Type", "text/html")
	w.Write(data)
}

// startWebServer registers HTTP handlers and starts the embedded web server.
func startWebServer() {
	http.HandleFunc("/", serveHTML)
	http.HandleFunc("/api/status", handleStatus)
	http.HandleFunc("/events", handleSSE)
	http.HandleFunc("/api/settings", handleSettings)
	http.HandleFunc("/api/update", handleUpdate)
	http.HandleFunc("/api/rescan", handleRescan)
	http.HandleFunc("/api/resize", handleResize)
	http.HandleFunc("/api/scan-hid", handleScanHID)
	// http.HandleFunc("/api/devtools/hid-report", handleHIDCapture)

	addr := ":" + serverPort
	if logger != nil {
		logger.Printf("[HTTP] listening on %s", addr)
	}
	if err := http.ListenAndServe(addr, nil); err != nil {
		if logger != nil {
			logger.Printf("[HTTP] server error: %v", err)
		} else {
			log.Printf("[HTTP] server error: %v", err)
		}
	}
}

func handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if f, ok := w.(http.Flusher); ok {
		fmt.Fprint(w, ":ok\n\n")
		// Send an immediate snapshot of the current state so new clients
		// see the battery status right away without waiting for the
		// next periodic update. Try to keep this consistent with recent
		// tray updates: if we have a recent non-last-known battery
		// reading (for example, a quick worker probe) treat that as a
		// present/connected state even when no persistent device handle
		// is held by the main process. This prevents a transient UI
		// mismatch where the tray shows charging but the main window
		// initially reports 'Not Connected'.
		lastKnownMu.Lock()
		showLK := showLastKnown
		lastKnownMu.Unlock()

		deviceMu.Lock()
		present := (device != nil || isWorkerManagedDevice())
		deviceMu.Unlock()

		// Only treat as present if we have an actual device handle or
		// worker-managed session. Don't use recentGood here to avoid
		// showing "connected" when device is actually disconnected.
		// The reading flag handles verification windows separately.

		initState := map[string]interface{}{
			"level":           batteryLvl,
			"charging":        isCharging,
			"status":          "disconnected",
			"statusText":      "Not Connected",
			"deviceModel":     deviceModel,
			"updateAvailable": updateAvailable,
			"updateVersion":   updateVersion,
		}
		// Indicate whether we're currently in a short verification window
		// after a connect so the UI can display 'Reading…' immediately.
		initState["reading"] = isReading()
		// Show lastKnown when we're displaying a cached value
		initState["lastKnown"] = showLK
		if present {
			initState["status"] = "connected"
			initState["statusText"] = "Connected"
			initState["lastChargeTime"] = lastChargeTime
			initState["lastChargeLevel"] = lastChargeLevel
		}
		if j, err := json.Marshal(initState); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", j)
		}
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
	// Ensure broadcasts always include an explicit lastKnown and reading flag.
	// Some call sites omit these keys; centralize the semantics here so UI
	// clients receive a consistent snapshot.
	lastKnownMu.Lock()
	showLK := showLastKnown
	lastKnownMu.Unlock()

	// If callers omitted the "reading" and/or "lastKnown" keys, provide
	// explicit defaults based on current state. Do not override explicit
	// values set by the caller.
	out := make(map[string]interface{}, len(data)+2)
	for k, v := range data {
		out[k] = v
	}
	if _, ok := out["lastKnown"]; !ok {
		out["lastKnown"] = showLK
	}
	if _, ok := out["reading"]; !ok {
		out["reading"] = isReading()
	}



	if logger != nil {
		// Log the primary keys we care about so we can trace UI/tray
		// divergence in the debug log without dumping the full payload.
		logger.Printf("[SSE] broadcast lastKnown=%v reading=%v status=%v level=%v",
			out["lastKnown"], out["reading"], out["status"], out["level"])
	}
	jsonData, _ := json.Marshal(out)
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

// setReading enables the "reading" verification window for duration d.
func setReading(d time.Duration) {
	readingMu.Lock()
	readingUntil = time.Now().Add(d)
	readingMu.Unlock()
}

// clearReading immediately clears any active verification window.
func clearReading() {
	readingMu.Lock()
	readingUntil = time.Time{}
	readingMu.Unlock()
}

// isReading returns true while the verification window is active.
func isReading() bool {
	readingMu.Lock()
	until := readingUntil
	readingMu.Unlock()
	if until.IsZero() {
		return false
	}
	return time.Now().Before(until)
}

// setForceLive enables a short grace window where recent live reads are
// treated as authoritative for UI presentation even if a transient
// disconnected broadcast follows.
func setForceLive(d time.Duration) {
	atomic.StoreInt64(&forceLiveUntilInt64, time.Now().Add(d).UnixNano())
}

func isForceLive() bool {
	v := atomic.LoadInt64(&forceLiveUntilInt64)
	if v == 0 {
		return false
	}
	return time.Now().UnixNano() < v
}

func startTray() {
	defer safeDefer("startTray")
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
	nid.HIcon = createBatteryIcon(0, false, false, 0)
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

	// Tray watchdog: periodically ping the tray thread and ensure it
	// responds. If it doesn't we dump goroutine stacks to help
	// diagnose root causes of a frozen tray/message loop.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			if hwnd == 0 {
				continue
			}
			// Post a ping and record when we posted it.
			lastTrayPingMu.Lock()
			lastTrayPing = time.Now()
			lastTrayPingMu.Unlock()
			postMessage := user32.NewProc("PostMessageW")
			postMessage.Call(uintptr(hwnd), WM_APP_TRAY_PING, 0, 0)

			// Allow a short window for the tray thread to respond.
			time.Sleep(2 * time.Second)

			lastTrayPongMu.Lock()
			lp := lastTrayPong
			lastTrayPongMu.Unlock()

			lastTrayPingMu.Lock()
			lq := lastTrayPing
			lastTrayPingMu.Unlock()

			if lp.Before(lq) {
				// Missing pong — increment counter and attempt recovery.
				watchdogNoPongCount++
				if logger != nil {
					logger.Printf("[TRAY_WATCHDOG] miss#%d: no pong within 2s (ping=%s lastPong=%s)", watchdogNoPongCount, lq.Format(time.RFC3339), lp.Format(time.RFC3339))
				}

				// Dump goroutines on the first miss to capture state.
				if watchdogNoPongCount == 1 {
					if logger != nil {
						buf := make([]byte, 1<<20)
						n := runtime.Stack(buf, true)
						logger.Printf("[TRAY_WATCHDOG] goroutine stack dump (%d bytes):\n%s", n, string(buf[:n]))
					}
				}

				// On repeated misses attempt an aggressive icon refresh
				// (delete then add) to recover Explorer-side state.
				if watchdogNoPongCount >= 2 {
					if logger != nil {
						logger.Printf("[TRAY_WATCHDOG] attempting icon refresh (delete+add) to recover tray interactivity")
					}
					// Try to delete the icon then re-add it. These calls
					// may succeed even if the tray thread is partially
					// hung; they can restore interactivity in many cases.
					if hwnd != 0 {
						win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
						time.Sleep(150 * time.Millisecond)
						win.Shell_NotifyIcon(win.NIM_ADD, &nid)
						nid.UVersion = win.NOTIFYICON_VERSION_4
						win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)
					}
				}
			} else {
				// Reset the miss counter when we receive a valid pong.
				watchdogNoPongCount = 0
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
	case WM_DEVICECHANGE:
		if logger != nil {
			logger.Printf("[DEVCHANGE] WM_DEVICECHANGE wParam=0x%X lParam=0x%X", wParam, lParam)
		}
		// Debounce device-change events and schedule a single stable
		// reconnect + non-intrusive quick refresh once enumeration settles.
		// Run asynchronously so the tray thread returns immediately.
		go scheduleDebouncedReconnect()

		// Quick immediate check: if our current path no longer enumerates
		// treat this as a disconnect and force a safe close so the tray
		// immediately shows a last-known/disconnected state instead of
		// waiting for longer probe timeouts. Run asynchronously so we
		// don't block the message loop.
		go func() {
			// Small quiesce so the OS has a moment to update enumerations.
			time.Sleep(120 * time.Millisecond)
			deviceMu.Lock()
			p := currentHIDPath
			hadDevice := (device != nil || workerManagedDevice)
			deviceMu.Unlock()

			// If we don't know the current path try a fast worker-backed
			// probe first so we can detect a newly-enumerated interface
			// quickly (e.g. switching wired ↔ wireless). If the quick
			// probe finds a usable reading it will call finishConnect and
			// update the UI; return early so we don't overwrite its result.
			if strings.TrimSpace(p) == "" {
				if tryImmediateWorkerQuickProbe() {
					return
				}
				lastKnownMu.Lock()
				lk := lastKnownLevel
				lkchg := lastKnownCharging
				lastKnownMu.Unlock()
				if !hadDevice && lk < 0 {
					return
				}
				if lk >= 0 {
					// Persist that we are showing a last-known value so
					// HTTP status and future broadcasts remain consistent.
					lastKnownMu.Lock()
					lastKnownLevel = lk
					lastKnownCharging = lkchg
					showLastKnown = true
					lastKnownMu.Unlock()
					if logger != nil {
						logger.Printf("[DEVCHANGE] immediate: no current path but showing last-known=%d%% (optimistic)", lk)
					}
					trayInvoke(func() {
						batteryLvl = lk
						isCharging = lkchg
						batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", lk)
						updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
						updateTrayIcon(lk, lkchg, true)
					})
					broadcast(map[string]interface{}{
						"level":           lk,
						"charging":        lkchg,
						"status":          "disconnected",
						"statusText":      "Last known",
						"lastKnown":       true,
						"lastChargeTime":  lastChargeTime,
						"lastChargeLevel": lastChargeLevel,
						"deviceModel":     deviceModel,
						"updateAvailable": updateAvailable,
						"updateVersion":   updateVersion,
					})
				}
				// Attempt a defensive safe close to allow reconnect to try a
				// fresh open if the system left a stale handle around.
				safeCloseDevice()
				return
			}
			if logger != nil {
				logger.Printf("[DEVCHANGE] immediate check scheduled for path %s", p)
			}
			// Show a quick "Last known" presentation immediately so the
			// UI doesn't briefly appear connected if the OS delays
			// enumeration updates. We'll still verify presence below
			// and perform an actual safeClose if necessary.
			lastKnownMu.Lock()
			lk := lastKnownLevel
			lkchg := lastKnownCharging
			lastKnownMu.Unlock()
			if lk >= 0 {
				// Persist that we are showing a last-known value so
				// HTTP status and future broadcasts remain consistent.
				lastKnownMu.Lock()
				lastKnownLevel = lk
				lastKnownCharging = lkchg
				showLastKnown = true
				lastKnownMu.Unlock()
				if logger != nil {
					logger.Printf("[DEVCHANGE] immediate: showing last-known=%d%% (optimistic) for path %s", lk, p)
				}
				trayInvoke(func() {
					batteryLvl = lk
					isCharging = lkchg
					batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", lk)
					updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
					updateTrayIcon(lk, lkchg, true)
				})
				// Broadcast an immediate last-known state so UI clients
				// reflect the disconnect quickly. The reconnect path will
				// emit a connected message if the device is still present.
				broadcast(map[string]interface{}{
					"level":           lk,
					"charging":        lkchg,
					"status":          "disconnected",
					"statusText":      "Last known",
					"lastKnown":       true,
					"lastChargeTime":  lastChargeTime,
					"lastChargeLevel": lastChargeLevel,
					"deviceModel":     deviceModel,
					"updateAvailable": updateAvailable,
					"updateVersion":   updateVersion,
				})
			}
			// Re-check presence for a short window to account for delayed
			// enumeration removals. If the path disappears during this
			// window, force a safe close so the UI updates promptly.
			maxAttempts := 6
			for i := 0; i < maxAttempts; i++ {
				if findDeviceInfoByPath(p) == nil {
					if logger != nil {
						logger.Printf("[DEVCHANGE] immediate: path %s no longer present (attempt=%d) — forcing safeCloseDevice", p, i)
					}
					// Perform an immediate UI/tray update + broadcast using the
					// last-known measurement so the UI doesn't briefly show 0%.
					lastKnownMu.Lock()
					lk := lastKnownLevel
					lkchg := lastKnownCharging
					lastKnownMu.Unlock()

					if lk >= 0 {
						// Update in the tray thread using the cached/dim icon
						trayInvoke(func() {
							batteryLvl = lk
							isCharging = lkchg
							batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", lk)
							updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
							updateTrayIcon(lk, lkchg, true)
						})
						broadcast(map[string]interface{}{
							"level":           lk,
							"charging":        lkchg,
							"status":          "disconnected",
							"statusText":      "Last known",
							"lastKnown":       true,
							"lastChargeTime":  lastChargeTime,
							"lastChargeLevel": lastChargeLevel,
							"deviceModel":     deviceModel,
							"updateAvailable": updateAvailable,
							"updateVersion":   updateVersion,
						})
					} else {
						// No last-known reading — show immediate disconnected
						trayInvoke(func() {
							batteryLvl = 0
							isCharging = false
							batteryText = "Mouse Not Found"
							updateTrayTooltip("Mouse Not Found")
							updateTrayIcon(0, false, false)
						})
						broadcast(map[string]interface{}{
							"level":           0,
							"charging":        false,
							"status":          "disconnected",
							"statusText":      "Disconnected",
							"deviceModel":     deviceModel,
							"updateAvailable": updateAvailable,
							"updateVersion":   updateVersion,
						})
					}

					// Now perform the usual safe close to clean up handles in
					// the background so the driver state is consistent.
					safeCloseDevice()
					return
				}
				time.Sleep(150 * time.Millisecond)
			}
			if logger != nil {
				logger.Printf("[DEVCHANGE] immediate check: path %s still present after %d checks", p, maxAttempts)
			}
		}()
		return 0
	case WM_APP_TRAY_MSG:
		// LOWORD(lParam) is the actual message code; we only need
		// minimal behavior: right-click shows the context menu and
		// left-click shows the main window.
		code := uint32(lParam) & 0xFFFF
		if code == win.WM_RBUTTONUP || code == WM_CONTEXTMENU {
			showMenu()
			return 0
		}
		if code == win.WM_LBUTTONUP || code == NIN_SELECT || code == NIN_KEYSELECT {
			showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
			win.SetForegroundWindow(webviewHwnd)
			return 0
		}
		// Ignore other tray messages
		return 0

	case win.WM_CONTEXTMENU:
		showMenu()
		return 0

	case WM_APP_TRAY_DO:
		// Drain the pending trayOps and execute them synchronously on the
		// tray thread. Tray operations perform GDI and Shell_NotifyIcon
		// calls which must be executed on the thread that owns the
		// tray/window resources. Running them on arbitrary goroutines can
		// cause cross-thread GDI issues and make the tray unresponsive
		// (observed when disconnecting devices).
		for {
			select {
			case fn := <-trayOps:
				// Execute each op on the tray thread with recovery so a
				// panicking tray operation cannot crash the message loop.
				func() {
					start := time.Now()
					defer func() {
						if r := recover(); r != nil {
							if logger != nil {
								logger.Printf("[TRAY_OP] op recovered: %v\n%s", r, debug.Stack())
							}
						}
						if logger != nil {
							dur := time.Since(start)
							if dur > 200*time.Millisecond {
								logger.Printf("[TRAY_OP] long-running op: %s", dur)
							}
						}
					}()
					fn()
				}()
			default:
				return 0
			}
		}

	case WM_APP_TRAY_PING:
		// Update last-pong timestamp so the watchdog knows the tray
		// thread is alive and processing messages.
		lastTrayPongMu.Lock()
		lastTrayPong = time.Now()
		lastTrayPongMu.Unlock()
		if logger != nil {
			logger.Printf("[TRAY_PONG] %s", lastTrayPong.Format(time.RFC3339))
		}
		return 0

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

	// Add a user-accessible rescan option so users can force a quick
	// refresh without opening the UI. This triggers the same logic as
	// the HTTP /api/rescan handler.
	rescanItem, _ := syscall.UTF16PtrFromString("Rescan")
	appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_RESCAN, uintptr(unsafe.Pointer(rescanItem)))

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
		// perform an idempotent, recover-wrapped close to avoid races
		safeCloseDevice()
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		terminateProcess := kernel32.NewProc("TerminateProcess")
		getCurrentProcess := kernel32.NewProc("GetCurrentProcess")
		handle, _, _ := getCurrentProcess.Call()
		terminateProcess.Call(handle, 0)
	}
}

// trayInvoke schedules a function to run on the tray thread and signals
// the tray window to process queued ops.
func trayInvoke(fn func()) {
	select {
	case trayOps <- fn:
	default:
		// If the buffer is full, ensure it still executes eventually.
		go func() { trayOps <- fn }()
	}
	if hwnd != 0 {
		postMessage := user32.NewProc("PostMessageW")
		postMessage.Call(uintptr(hwnd), WM_APP_TRAY_DO, 0, 0)
	}
}

func updateBattery() {
	defer safeDefer("updateBattery")
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("updateBattery recovered from panic: %v", r)
			}
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
		<-ticker.C
	}
}

func updateBatteryOLD() {
	defer safeDefer("updateBattery")
	defer func() {
		if r := recover(); r != nil {
			if logger != nil {
				logger.Printf("updateBattery recovered from panic: %v", r)
			}
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

		if device != nil || isWorkerManagedDevice() {
			battery, charging := readBattery()

			if linkDown {
				softLinkDownCount++
				// Save charge data when unplugging while charging
				if wasCharging && softLinkDownCount == 1 && !recordedUnplug {
					// choose a sensible last-known level:
					// 1) current on-screen level if >0, else
					// 2) last charging track level if we have one, else
					// 3) keep previous lastChargeLevel
					lvl := batteryLvl
					if lvl <= 0 && lastChargeLevel2 > 0 {
						lvl = lastChargeLevel2
					}
					// Apply same validation as normal charge completion
					if lvl > 0 && (lvl >= lastChargeLevel || lvl >= 10) {
						lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
						lastChargeLevel = lvl
						saveChargeData()
						recordedUnplug = true
						if logger != nil {
							logger.Printf("[CHARGE] saved charge completion on unplug: time=%s level=%d (wasCharging=%v)", lastChargeTime, lastChargeLevel, wasCharging)
						}
					}
				}

				// Immediately show a recent last-known reading when the
				// link goes down so the UI doesn't briefly cycle through
				// 0%/Disconnected before settling on the last known value.
				lastKnownMu.Lock()
				lk := lastKnownLevel
				lkchg := lastKnownCharging
				if lk >= 0 {
					showLastKnown = true
				}
				lastKnownMu.Unlock()

				if lk >= 0 {
					// Present the last-known value right away and continue
					// attempting reconnects in the background.
					batteryLvl = lk
					isCharging = lkchg
					batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", lk)
					updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
					updateTrayIcon(lk, lkchg, true)
					broadcast(map[string]interface{}{
						"level":           lk,
						"charging":        lkchg,
						"status":          "disconnected",
						"statusText":      "Last known",
						"lastKnown":       true,
						"lastChargeTime":  lastChargeTime,
						"lastChargeLevel": lastChargeLevel,
						"deviceModel":     deviceModel,
						"updateAvailable": updateAvailable,
						"updateVersion":   updateVersion,
					})

					// Schedule a short forced close if this stale last-known state
					// persists so reconnect may attempt a fresh open instead of
					// remaining stuck on a stale handle.
					scheduleForceCloseIfStale(currentHIDPath)
					<-ticker.C
					continue
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
					updateTrayIcon(0, false, false)
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

						// perform a safe close which will wait for the reader and
						// reset global device/input state in a race-free way
						safeCloseDevice()
					}
				}
				<-ticker.C
				continue
			} else {
				softLinkDownCount = 0
			}

			// battery == -1 means "invalid/no data"
			if battery >= 0 {
				// Detect an immediate, large drop compared to the currently
				// displayed level and treat it as suspicious. In that case
				// preserve the last-known/UI display and run a short
				// confirmation routine so we avoid accepting single-sample
				// spurious lows (e.g. 45% → 3%).
				prevDisplayed := batteryLvl
				if prevDisplayed > 0 && battery < prevDisplayed && prevDisplayed >= 15 && (prevDisplayed-battery) >= 20 {
					dropConfirmMu.Lock()
					already := dropConfirmActive
					if !already {
						dropConfirmActive = true
					}
					dropConfirmMu.Unlock()
					if !already {
						if logger != nil {
							logger.Printf("[READSUPP] detected large drop (prev=%d → new=%d) — preserving last-known and verifying", prevDisplayed, battery)
						}
						// Keep showing the last-known value while verifying
						setReading(4 * time.Second)
						lastKnownMu.Lock()
						lk := lastKnownLevel
						lkchg := lastKnownCharging
						lastKnownMu.Unlock()
						if lk >= 0 {
							trayInvoke(func() {
								batteryLvl = lk
								isCharging = lkchg
								batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", lk)
								updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
								updateTrayIcon(lk, lkchg, true)
							})
							broadcast(map[string]interface{}{
								"status":          "connected",
								"mode":            map[bool]string{true: "Charging", false: "Discharging"}[lkchg],
								"statusText":      "Verifying…",
								"level":           lk,
								"charging":        lkchg,
								"lastKnown":       true,
								"lastChargeTime":  lastChargeTime,
								"lastChargeLevel": lastChargeLevel,
								"deviceModel":     deviceModel,
								"updateAvailable": updateAvailable,
								"updateVersion":   updateVersion,
								"reading":         true,
							})

							// Spawn background confirmation that will accept the
							// new, low reading only if observed consistently.
							go func(expected int, chg bool, prev int) {
								defer func() {
									dropConfirmMu.Lock()
									dropConfirmActive = false
									dropConfirmMu.Unlock()
								}()
								confirmations := 0
								attempts := 3
								for i := 0; i < attempts; i++ {
									time.Sleep(220 * time.Millisecond)
									if lvl2, _ := readBattery(); lvl2 >= 0 {
										if absInt(lvl2-expected) <= 3 {
											confirmations++
										}
									}
								}
								if confirmations >= 2 {
									// Confirmed: accept the low value
									batteryLvl = expected
									isCharging = chg
									// Clear any persisted last-known presentation
									lastKnownMu.Lock()
									showLastKnown = false
									lastKnownLevel = expected
									lastKnownCharging = chg
									lastKnownMu.Unlock()
									updateTrayTooltip(fmt.Sprintf("Battery: %d%%", expected))
									updateTrayIcon(expected, chg, false)
									if logger != nil {
										logger.Printf("[READSUPP] large-drop confirmed: prev=%d accepted=%d", prev, expected)
									}
									clearReading()
									broadcast(map[string]interface{}{"status": "connected", "reading": false, "level": batteryLvl, "charging": isCharging})
									return
								}
								// Not confirmed: keep last-known display
								lastKnownMu.Lock()
								lk2 := lastKnownLevel
								lkchg2 := lastKnownCharging
								lastKnownMu.Unlock()
								if lk2 >= 0 {
									batteryLvl = lk2
									isCharging = lkchg2
									updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk2))
									updateTrayIcon(lk2, lkchg2, true)
									if logger != nil {
										logger.Printf("[READSUPP] large-drop NOT confirmed — keeping last-known=%d", lk2)
									}
									clearReading()
									broadcast(map[string]interface{}{"status": "connected", "lastKnown": true, "level": lk2, "charging": lkchg2, "reading": false})
								}
							}(battery, charging, prevDisplayed)
							<-ticker.C
							continue
						}
					}
				}
				// Remember this as the most recent valid reading so the UI can
				// show it if the device later disconnects.
				lastKnownMu.Lock()
				lastKnownLevel = battery
				lastKnownCharging = charging
				showLastKnown = false
				lastKnownMu.Unlock()
				atomic.StoreInt64(&lastGoodReadUnix, time.Now().UnixNano())
				// Ensure we preserve the freshly-observed reading for a short window
				// so transient races don't immediately flip the UI to disconnected.
				setForceLive(6000 * time.Millisecond)
				consecutiveReadFails = 0
				recordedUnplug = false

				// Detect charge completion: wasCharging was true, now charging is false
				if wasCharging && !charging && battery > 0 {
					// avoid recording an obviously bogus "last charged at 1–2%" blip
					if battery >= lastChargeLevel || battery >= 10 {
						lastChargeTime = time.Now().Format("Jan 2, 3:04 PM")
						lastChargeLevel = battery
						saveChargeData()
						if logger != nil {
							logger.Printf("[CHARGE] saved charge completion: time=%s level=%d (wasCharging=%v charging=%v)", lastChargeTime, lastChargeLevel, wasCharging, charging)
						}
					}
				}

				// Update wasCharging for next iteration
				wasCharging = charging

				// Track charging state and rates
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

				// If we're currently in a post-connect verification window and
				// we observe a single 0% reading while the UI previously
				// displayed a non-zero level, treat this 0% as suspicious and
				// do not accept it immediately. Instead, keep showing the
				// previous known (non-zero) value and mark the UI as still
				// "reading" until verification expires or confirms.
				if battery == 0 && isReading() && batteryLvl > 0 {
					if logger != nil {
						logger.Printf("[READSUPP] suppressing single 0%% read while verifying (prev=%d) path=%s", batteryLvl, currentHIDPath)
					}
					// Keep previous globals and re-broadcast the previous value
					updateTrayTooltip(fmt.Sprintf("Battery: %d%%", batteryLvl))
					updateTrayIcon(batteryLvl, isCharging, false)
					broadcast(map[string]interface{}{
						"status":          "connected",
						"mode":            map[bool]string{true: "Charging", false: "Discharging"}[isCharging],
						"statusText":      map[bool]string{true: "Charging", false: "Discharging"}[isCharging],
						"level":           batteryLvl,
						"charging":        isCharging,
						"lastChargeTime":  lastChargeTime,
						"lastChargeLevel": lastChargeLevel,
						"deviceModel":     deviceModel,
						"updateAvailable": updateAvailable,
						"updateVersion":   updateVersion,
						"reading":         true,
					})
					// Skip the rest of this update cycle; allow finishConnect's
					// verification window to continue.
					<-ticker.C
					continue
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
				updateTrayIcon(battery, charging, false)

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
					"reading":         isReading(),
				})
			} else {
				// invalid read → force reconnect next tick
				batteryLvl = 0
				isCharging = false
				batteryText = "Connecting..."
				updateTrayTooltip("Connecting…")
				updateTrayIcon(0, false, false)
				broadcast(map[string]interface{}{
					"level":      0,
					"charging":   false,
					"status":     "connecting",
					"statusText": "Connecting",
				})
			}
		} else {
			// Device not found — prefer showing last-known info if we have
			// a recent valid reading. This keeps the UI useful when the
			// device briefly disconnects during re-enumeration.
			lastKnownMu.Lock()
			lk := lastKnownLevel
			lkchg := lastKnownCharging
			shouldShow := showLastKnown && lk >= 0
			lastKnownMu.Unlock()
			if shouldShow {
				batteryLvl = lk
				isCharging = lkchg
				batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", lk)
				updateTrayTooltip(fmt.Sprintf("Last known: %d%%", lk))
				updateTrayIcon(lk, lkchg, true)
				broadcast(map[string]interface{}{
					"level":           lk,
					"charging":        lkchg,
					"status":          "disconnected",
					"statusText":      "Last known",
					"lastKnown":       true,
					"lastChargeTime":  lastChargeTime,
					"lastChargeLevel": lastChargeLevel,
				})
			} else {
				batteryLvl = 0
				isCharging = false
				batteryText = "Mouse Not Found"
				updateTrayTooltip("Mouse Not Found")
				updateTrayIcon(0, false, false)
				broadcast(map[string]interface{}{
					"level":      0,
					"charging":   false,
					"status":     "disconnected",
					"statusText": "Disconnected",
				})
			}
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

func createBatteryIcon(level int, charging bool, dim bool, frame int) win.HICON {
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
	if dim {
		// muted gray for last-known/disconnected state
		fillColor = 0xFF6E7681
	} else if charging {
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

func updateTrayIcon(level int, charging bool, dim bool) {
	trayInvoke(func() {
		// Fast path: when we're showing a muted/"dim" (last-known /
		// disconnected) icon, reuse a cached HICON so we avoid
		// regenerating a full DIB-backed icon on each disconnect which
		// can block the tray message loop.
		if dim {
			// Load or create the cached disconnected icon on the tray
			// thread. Double-check under the cache mutex to avoid races
			// if other code ever touches the cache from outside the
			// tray thread.
			cachedIconMu.Lock()
			ci := cachedDisconnectedIcon
			cachedIconMu.Unlock()

			if ci == 0 {
				// Create the canonical dim icon (use level=0/charging=false)
				// and store it in the cache. Creating it here on the tray
				// thread is acceptable — it happens once and subsequent
				// disconnects are fast.
				newCi := createBatteryIcon(0, false, true, 0)
				if newCi != 0 {
					cachedIconMu.Lock()
					if cachedDisconnectedIcon == 0 {
						cachedDisconnectedIcon = newCi
						ci = newCi
					} else {
						// Another closure created the icon first; schedule
						// our temporary icon for reap and use the cached one.
						select {
						case iconReap <- newCi:
						default:
						}
						ci = cachedDisconnectedIcon
					}
					cachedIconMu.Unlock()
				}
			}

			if ci != 0 {
				trayMu.Lock()
				oldIcon := nid.HIcon
				nid.HIcon = ci
				// IMPORTANT: include NIF_TIP and SHOWTIP so we don't stomp tooltip updates.
				nid.UFlags = win.NIF_ICON | win.NIF_TIP | 0x00000080
				win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
				nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
				trayMu.Unlock()

				// Reap the old icon only if it's not the cached one.
				if oldIcon != 0 && oldIcon != ci {
					select {
					case iconReap <- oldIcon:
					default:
					}
				}
				return
			}
			// If we couldn't create a cached icon for some reason fall
			// back to the full dynamic path below.
		}

		// Dynamic icon path for normal (non-dim) updates. Use a cache so
		// we avoid repeating expensive GDI/DIB work for common level
		// states and animation frames.
		key := fmt.Sprintf("%03d:%t:%d", level, charging, animationFrame)
		iconCacheMu.Lock()
		cachedIcon, ok := iconCache[key]
		iconCacheMu.Unlock()
		var newIcon win.HICON
		if ok && cachedIcon != 0 {
			newIcon = cachedIcon
		} else {
			newIcon = createBatteryIcon(level, charging, dim, animationFrame)
			if newIcon == 0 {
				return
			}
			iconCacheMu.Lock()
			iconCache[key] = newIcon
			iconCacheMu.Unlock()
		}
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

		// Reap old icon only if it is not the cached disconnected icon
		// AND not present in the iconCache. We purposely keep cached
		// icons alive.
		cachedIconMu.Lock()
		cached := cachedDisconnectedIcon
		cachedIconMu.Unlock()
		if oldIcon != 0 && oldIcon != newIcon && oldIcon != cached {
			// Check if oldIcon is still referenced in iconCache
			keepon := false
			iconCacheMu.Lock()
			for _, v := range iconCache {
				if v == oldIcon {
					keepon = true
					break
				}
			}
			iconCacheMu.Unlock()
			if !keepon {
				select {
				case iconReap <- oldIcon:
				default:
				}
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
	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if isCharging {
				animationFrame = (animationFrame + 1) % 3
				updateTrayIcon(batteryLvl, isCharging, false)
			} else {
				animationFrame = 0
			}
		case <-stopAnimation:
			return
		}
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

// handleRescan triggers an immediate device rescan/probe and returns
// quickly so the UI (or a user) can request a manual rescan.
func handleRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if logger != nil {
		logger.Printf("[HTTP] manual rescan requested")
	}
	go func() {
		time.Sleep(80 * time.Millisecond)
		// Clear transient backoffs caused by prior probe attempts so
		// manual rescans don't wait for blacklist timers to expire.
		clearBackoffsForCandidates()
		// Ensure the probe worker is running for a non-intrusive scan.
		_ = StartProbeWorker()
		reconnect()
		// Try an immediate, conservative worker-backed quick probe so the
		// manual rescan updates the UI promptly if a device is present.
		_ = tryImmediateWorkerQuickProbe()
	}()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"started": true})
}

// handleScanHID performs a comprehensive HID device scan and returns
// detailed information about all connected HID devices, with special
// focus on Glorious devices. Works independently of current device status.
func handleScanHID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if logger != nil {
		logger.Printf("[HTTP] HID device scan requested")
	}

	// Perform the scan
	result := scanAllHIDDevices()

	// Log results to debug log
	logHIDScanResults(result)

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		if logger != nil {
			logger.Printf("[HTTP] Failed to encode HID scan results: %v", err)
		}
		http.Error(w, "Failed to encode results", http.StatusInternalServerError)
		return
	}

	if logger != nil {
		logger.Printf("[HTTP] HID scan completed: %d total devices, %d Glorious devices", result.TotalCount, result.GloriousCount)
	}
}

// handleStatus returns the current battery/connect state as JSON so
// the web UI can perform a reliable one-shot fetch of the latest
// state when it loads (supplementing the streaming SSE updates).
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Snapshot relevant state under locks where needed
	lastKnownMu.Lock()
	showLK := showLastKnown
	lastKnownMu.Unlock()

	deviceMu.Lock()
	devPresent := (device != nil || isWorkerManagedDevice())
	curPath := currentHIDPath
	deviceMu.Unlock()

	status := "disconnected"
	statusText := "Not Connected"
	if devPresent {
		status = "connected"
		statusText = "Connected"
	}

	resp := map[string]interface{}{
		"status":           status,
		"statusText":       statusText,
		"level":            batteryLvl,
		"charging":         isCharging,
		"reading":          isReading(),
		"lastKnown":        showLK,
		"lastChargeTime":   lastChargeTime,
		"lastChargeLevel":  lastChargeLevel,
		"deviceModel":      deviceModel,
		"updateAvailable":  updateAvailable,
		"updateVersion":    updateVersion,
		"path":             curPath,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// quickRefreshOnDeviceChange performs several fast read attempts after a
// device change event so charging/unplug transitions are published more
// promptly to the UI instead of waiting for the next scheduled tick.
func quickRefreshOnDeviceChange() {
	go func() {
		// Wait a short moment so enumeration has time to stabilize.
		time.Sleep(350 * time.Millisecond)
		// Prefer using the worker to perform a non-intrusive probe so
		// we avoid local opens/writes immediately after re-enumeration.
		// Read current path under the device mutex to avoid races with
		// the reader/close logic. If the current path no longer
		// enumerates, clear it so we probe fresh candidates instead of
		// repeatedly probing a stale path.
		deviceMu.Lock()
		path := currentHIDPath
		deviceMu.Unlock()
		if path != "" {
			if findDeviceInfoByPath(path) == nil {
				if logger != nil {
					logger.Printf("[DEVCHANGE] cached currentHIDPath %s no longer enumerates — forcing candidate scan", path)
				}
				path = ""
				// Try an immediate quick probe for any newly-enumerated
				// candidate before showing a last-known presentation.
				if tryImmediateWorkerQuickProbe() {
					return
				}
			}
		}
		if path == "" && len(cachedProfiles) > 0 {
			path = cachedProfiles[0].Path
		}
		if path == "" {
			// If we don't have a current path, probe likely candidates
			// (wireless/receiver interfaces and devices that match the
			// known deviceModel) so charging state changes that appear on
			// a different interface are detected quickly.
			// Ensure the helper is running so we can perform non-intrusive probes.
			if getProbeWorker() == nil {
				if err := StartProbeWorker(); err != nil || getProbeWorker() == nil {
					return
				}
			}
			// Build a prioritized list of candidate paths to probe.
			tried := make(map[string]bool)
			var candidates []string
			lowModel := strings.ToLower(deviceModel)
			for _, vid := range gloriousVendorIDs {
				hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
					if shouldSkipCandidate(info) {
						return nil
					}
					if tried[info.Path] {
						return nil
					}
					// Prefer exact model matches first so we don't probe a
					// large number of unrelated devices.
					if lowModel != "" && strings.Contains(strings.ToLower(info.ProductStr), lowModel) {
						candidates = append([]string{info.Path}, candidates...)
						tried[info.Path] = true
						return nil
					}
					if qualifiesForWorkerManaged(info) {
						candidates = append(candidates, info.Path)
						tried[info.Path] = true
					}
					return nil
				})
			}
			// If we still have no good candidates, do a broader scan.
			if len(candidates) == 0 {
				hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
					if shouldSkipCandidate(info) {
						return nil
					}
					if tried[info.Path] {
						return nil
					}
					if qualifiesForWorkerManaged(info) {
						candidates = append(candidates, info.Path)
						tried[info.Path] = true
					}
					return nil
				})
			}
			// Probe each candidate until we get a usable report.
			for _, p := range candidates {
				if w := getProbeWorker(); w != nil {
					if wlvl, wchg, wok, wrid, wlen, werr := w.ProbePathAll(p); werr == nil && wok {
						// If we recently have a last-known value and the
						// quick worker probe reports a reading that is
						// drastically lower than that last-known value,
						// treat the new reading as suspicious and require
						// confirmation before accepting it. This prevents
						// spurious single-sample flips (e.g. 45% -> 3%).
						lastKnownMu.Lock()
						lk := lastKnownLevel
						lastKnownMu.Unlock()

						// Combined suspiciousness check: prefer the current
						// displayed reading, otherwise consider a recent good
						// lastKnown reading. If the quick probe is far lower
						// than a recent good reading, defer acceptance and
						// verify it with a short confirmation routine.
						displayed := batteryLvl
						displayedChg := isCharging
						suspect := false
						hold := 0
						holdchg := false
						if displayed >= 15 && (displayed-wlvl) >= 20 {
							suspect = true
							hold = displayed
							holdchg = displayedChg
						} else if lk >= 15 && (lk-wlvl) >= 20 {
							suspect = true
							hold = lk
							holdchg = lastKnownCharging
						}
						if suspect {
							if logger != nil {
								logger.Printf("[DEVCHANGE] quick probe on %s returned low lvl=%d while recent=%d — deferring acceptance and verifying", p, wlvl, hold)
							}
							setReading(3 * time.Second)
							trayInvoke(func() {
								batteryLvl = hold
								isCharging = holdchg
								batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", hold)
								updateTrayTooltip(fmt.Sprintf("Last known: %d%%", hold))
								updateTrayIcon(hold, holdchg, true)
							})
							broadcast(map[string]interface{}{
								"status":          "connected",
								"mode":            map[bool]string{true: "Charging", false: "Discharging"}[holdchg],
								"statusText":      "Verifying…",
								"level":           hold,
								"charging":        holdchg,
								"lastKnown":       true,
								"lastChargeTime":  lastChargeTime,
								"lastChargeLevel": lastChargeLevel,
								"deviceModel":     deviceModel,
								"updateAvailable": updateAvailable,
								"updateVersion":   updateVersion,
								"reading":         true,
							})
							go func(path string, expected int, w *WorkerClient, rid byte, rlen int, chg bool, hold int, holdchg bool) {
								defer safeDefer("quickProbeConfirm")
								confirm := 0
								attempts := 3
								for i := 0; i < attempts; i++ {
									time.Sleep(220 * time.Millisecond)
									if wlvl2, _, wok2, _, _, werr2 := w.ProbePathAll(path); werr2 == nil && wok2 {
										if absInt(wlvl2-expected) <= 3 {
											confirm++
										}
										if confirm >= 2 {
											selectedReportID = rid
											selectedReportLen = rlen
											useGetOnly = true
											useInputReports = false
											batteryLvl = expected
											isCharging = chg
											// Clear any persisted last-known presentation
											lastKnownMu.Lock()
											showLastKnown = false
											lastKnownLevel = expected
											lastKnownCharging = chg
											lastKnownMu.Unlock()
											updateTrayTooltip(fmt.Sprintf("Battery: %d%%", expected))
											updateTrayIcon(expected, chg, false)
											if logger != nil {
												logger.Printf("[DEVCHANGE] quick probe on %s confirmed lvl=%d chg=%v", path, expected, chg)
											}
											clearReading()
											broadcast(map[string]interface{}{
												"status":          "connected",
												"mode":            map[bool]string{true: "Charging", false: "Discharging"}[chg],
												"statusText":      map[bool]string{true: "Charging", false: "Discharging"}[chg],
												"level":           batteryLvl,
												"charging":        isCharging,
												"lastKnown":       false,
												"lastChargeTime":  lastChargeTime,
												"lastChargeLevel": lastChargeLevel,
												"deviceModel":     deviceModel,
												"updateAvailable": updateAvailable,
												"updateVersion":   updateVersion,
												"reading":         false,
											})
											return
										}
									}
								}
								if logger != nil {
									logger.Printf("[DEVCHANGE] quick probe on %s failed confirmation; keeping hold=%d", path, hold)
								}
								clearReading()
								broadcast(map[string]interface{}{
									"status":     "connected",
									"mode":       map[bool]string{true: "Charging", false: "Discharging"}[holdchg],
									"statusText": "Verifying failed",
									"level":      hold,
									"charging":   holdchg,
									"lastKnown":  true,
									"reading":    false,
								})
							}(p, wlvl, w, wrid, wlen, wchg, hold, holdchg)
							return
						}

						// Not suspicious — accept immediately.
						selectedReportID = wrid
						selectedReportLen = wlen
						useGetOnly = true
						useInputReports = false
						saveConnProfile(DeviceProfile{
							Path:            p,
							ReportID:        selectedReportID,
							ReportLen:       selectedReportLen,
							UseGetOnly:      useGetOnly,
							UseInputReports: useInputReports,
						})
						batteryLvl = wlvl
						isCharging = wchg
						// Clear any prior recorded write failures for this path — a
						// successful probe means the path is responding to reads.
						clearWriteFailures(p)
						// Persist as a cached profile so reconnect() can pick
						// this up on the next loop and avoid long backoff.
						saveConnProfile(DeviceProfile{
							Path:            p,
							ReportID:        selectedReportID,
							ReportLen:       selectedReportLen,
							UseGetOnly:      useGetOnly,
							UseInputReports: useInputReports,
						})
						if logger != nil {
							logger.Printf("[DEVCHANGE] quick worker probe succeeded on %s lvl=%d chg=%v", p, wlvl, wchg)
						}

						// Broadcast the quick result so UI updates immediately.
						status := map[bool]string{true: "Charging", false: "Discharging"}[wchg]
						icon := "🔋"
						if wchg {
							icon = "⚡"
						}
						batteryText = fmt.Sprintf("%s %d%% (%s)", icon, wlvl, status)
						updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl))
						updateTrayIcon(wlvl, wchg, false)
						broadcast(map[string]interface{}{
							"status":          "connected",
							"mode":            status,
							"statusText":      status,
							"level":           wlvl,
							"charging":        wchg,
							"lastChargeTime":  lastChargeTime,
							"lastChargeLevel": lastChargeLevel,
							"deviceModel":     deviceModel,
							"updateAvailable": updateAvailable,
							"updateVersion":   updateVersion,
							"reading":         false,
						})
						return
					}
				}
			}
			return
		}
		if getProbeWorker() == nil {
			if err := StartProbeWorker(); err != nil || getProbeWorker() == nil {
				return
			}
		}
		if w := getProbeWorker(); w != nil {
			if wlvl, wchg, wok, wrid, wlen, werr := w.ProbePathAll(path); werr == nil && wok {
				// Decide whether this quick probe is suspicious compared to a
				// recent good reading. Prefer the currently-displayed value
				// first, fall back to a recent lastKnown value if available.
				lastKnownMu.Lock()
				lk := lastKnownLevel
				lastKnownMu.Unlock()

				displayed := batteryLvl
				displayedChg := isCharging
				suspect := false
				hold := 0
				holdchg := false
				if displayed >= 15 && (displayed-wlvl) >= 20 {
					suspect = true
					hold = displayed
					holdchg = displayedChg
				} else if lk >= 15 && (lk-wlvl) >= 20 {
					suspect = true
					hold = lk
					holdchg = lastKnownCharging
				}
				if suspect {
					if logger != nil {
						logger.Printf("[DEVCHANGE] quick probe on %s returned low lvl=%d while recent=%d — deferring acceptance and verifying", path, wlvl, hold)
					}
					setReading(3 * time.Second)
					trayInvoke(func() {
						batteryLvl = hold
						isCharging = holdchg
						batteryText = fmt.Sprintf("Last: %d%% (Disconnected)", hold)
						updateTrayTooltip(fmt.Sprintf("Last known: %d%%", hold))
						updateTrayIcon(hold, holdchg, true)
					})
					broadcast(map[string]interface{}{
						"status":          "connected",
						"mode":            map[bool]string{true: "Charging", false: "Discharging"}[holdchg],
						"statusText":      "Verifying…",
						"level":           hold,
						"charging":        holdchg,
						"lastKnown":       true,
						"lastChargeTime":  lastChargeTime,
						"lastChargeLevel": lastChargeLevel,
						"deviceModel":     deviceModel,
						"updateAvailable": updateAvailable,
						"updateVersion":   updateVersion,
						"reading":         true,
					})

					// Confirm the low reading with a few quick worker probes
					// before accepting it.
					go func(path string, expected int, w *WorkerClient, rid byte, rlen int, chg bool, hold int, holdchg bool) {
						defer safeDefer("quickProbeConfirm")
						confirm := 0
						attempts := 3
						for i := 0; i < attempts; i++ {
							time.Sleep(220 * time.Millisecond)
							if wlvl2, _, wok2, _, _, werr2 := w.ProbePathAll(path); werr2 == nil && wok2 {
								if absInt(wlvl2-expected) <= 3 {
									confirm++
								}
								if confirm >= 2 {
									selectedReportID = rid
									selectedReportLen = rlen
									useGetOnly = true
									useInputReports = false
									batteryLvl = expected
									isCharging = chg
									// Clear persisted last-known so clients show live
									lastKnownMu.Lock()
									showLastKnown = false
									lastKnownLevel = expected
									lastKnownCharging = chg
									lastKnownMu.Unlock()
									updateTrayTooltip(fmt.Sprintf("Battery: %d%%", expected))
									updateTrayIcon(expected, chg, false)
									if logger != nil {
										logger.Printf("[DEVCHANGE] quick probe on %s confirmed lvl=%d chg=%v", path, expected, chg)
									}
									clearReading()
									broadcast(map[string]interface{}{
										"status":          "connected",
										"mode":            map[bool]string{true: "Charging", false: "Discharging"}[chg],
										"statusText":      map[bool]string{true: "Charging", false: "Discharging"}[chg],
										"level":           batteryLvl,
										"charging":        isCharging,
										"lastKnown":       false,
										"lastChargeTime":  lastChargeTime,
										"lastChargeLevel": lastChargeLevel,
										"deviceModel":     deviceModel,
										"updateAvailable": updateAvailable,
										"updateVersion":   updateVersion,
										"reading":         false,
									})
									return
								}
							}
						}
						if logger != nil {
							logger.Printf("[DEVCHANGE] quick probe on %s failed confirmation; keeping hold=%d", path, hold)
						}
						clearReading()
						broadcast(map[string]interface{}{
							"status":     "connected",
							"mode":       map[bool]string{true: "Charging", false: "Discharging"}[holdchg],
							"statusText": "Verifying failed",
							"level":      hold,
							"charging":   holdchg,
							"lastKnown":  true,
							"reading":    false,
						})
					}(path, wlvl, w, wrid, wlen, wchg, hold, holdchg)
					return
				}

				// Not suspicious — publish result immediately.
				selectedReportID = wrid
				selectedReportLen = wlen
				useGetOnly = true
				useInputReports = false
				batteryLvl = wlvl
				isCharging = wchg
				// Clear any prior write failures for the detected path.
				clearWriteFailures(path)
				if logger != nil {
					logger.Printf("[DEVCHANGE] quick worker probe succeeded on %s lvl=%d chg=%v", path, wlvl, wchg)
				}

				// Broadcast the quick result so UI updates immediately.
				status := map[bool]string{true: "Charging", false: "Discharging"}[wchg]
				icon := "🔋"
				if wchg {
					icon = "⚡"
				}
				batteryText = fmt.Sprintf("%s %d%% (%s)", icon, wlvl, status)
				// Clear persisted last-known so the UI treats this as a live
				// reading rather than a last-known presentation.
				lastKnownMu.Lock()
				showLastKnown = false
				lastKnownLevel = wlvl
				lastKnownCharging = wchg
				lastKnownMu.Unlock()
				updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl))
				updateTrayIcon(wlvl, wchg, false)
				broadcast(map[string]interface{}{
					"status":          "connected",
					"mode":            status,
					"statusText":      status,
					"level":           wlvl,
					"charging":        wchg,
					"lastKnown":       false,
					"lastChargeTime":  lastChargeTime,
					"lastChargeLevel": lastChargeLevel,
					"deviceModel":     deviceModel,
					"updateAvailable": updateAvailable,
					"updateVersion":   updateVersion,
					"reading":         false,
				})
				return
			}
		}
	}()
}

// scheduleDebouncedReconnect coalesces multiple WM_DEVICECHANGE events and
// schedules a single reconnect + quick refresh once enumeration stabilizes.
func scheduleDebouncedReconnect() {
	// Record the last change timestamp atomically so the tray thread never
	// blocks acquiring a mutex. Use CAS to ensure only one scheduled
	// debounced worker exists at a time.
	atomic.StoreInt64(&lastDevChangeUnix, time.Now().UnixNano())
	if !atomic.CompareAndSwapInt32(&devChangeScheduledInt, 0, 1) {
		return
	}

	go func() {
		// Debounce: wait until enumeration settles (no further devchange
		// events within the small window) then perform reconnect.
		for {
			time.Sleep(300 * time.Millisecond)
			since := time.Since(time.Unix(0, atomic.LoadInt64(&lastDevChangeUnix)))
			if since < 900*time.Millisecond {
				continue
			}
			// Mark as unscheduled and request a fresh probe for reconnect.
			atomic.StoreInt32(&devChangeScheduledInt, 0)
			atomic.StoreInt32(&forceFreshProbeOnceInt, 1)

			if logger != nil {
				logger.Printf("[DEVCHANGE] stable — performing reconnect and quick refresh")
			}
			clearBackoffsForCandidates()
			reconnect()
			quickRefreshOnDeviceChange()
			return
		}
	}()
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

func finishConnect(path string, lvl int, chg bool) {
	recordedUnplug = false
	// Cancel any delayed read-only fallback and scheduled force-close for
	// this path now that we've successfully connected/initialized it.
	cancelDelayedReadOnlyLocalFallback(path)
	cancelScheduledForceClose(path)
	saveConnProfile(DeviceProfile{
		Path:            path,
		ReportID:        selectedReportID,
		ReportLen:       selectedReportLen,
		UseGetOnly:      useGetOnly,
		UseInputReports: useInputReports,
	})
	// If the immediate read reports 0 but we have a previously-stored
	// last-known reading, preserve that value for the short verification
	// window so the UI doesn't immediately flip to 0%.
	preserveLastKnown := false
	lastKnownMu.Lock()
	lk := lastKnownLevel
	lastKnownMu.Unlock()
	if lvl == 0 && lk > 0 {
		batteryLvl = lk
		// charging status unknown for stored readings — prefer the
		// freshly-reported charging flag only when non-zero is read.
		isCharging = chg
		preserveLastKnown = true
		lastKnownMu.Lock()
		showLastKnown = true
		lastKnownMu.Unlock()
	} else {
		// If the immediate read is wildly lower than our last-known
		// value treat it as suspicious and preserve the last-known
		// reading until we confirm the new low reading. This avoids
		// accepting a spurious 3% reading when we recently had ~40%.
		if lk > 0 && lvl > 0 {
			// Conservative rule: preserve when lastKnown >= 15 and the
			// new reading is at least 20 percentage points lower than
			// the last-known value.
			if lk >= 15 && (lk-lvl) >= 20 {
				batteryLvl = lk
				isCharging = chg
				preserveLastKnown = true
				lastKnownMu.Lock()
				showLastKnown = true
				lastKnownMu.Unlock()
			} else {
				batteryLvl = lvl
				isCharging = chg
				lastKnownMu.Lock()
				showLastKnown = false
				lastKnownMu.Unlock()
			}
		} else {
			batteryLvl = lvl
			isCharging = chg
			lastKnownMu.Lock()
			showLastKnown = false
			lastKnownMu.Unlock()
		}
	}
	status := "Discharging"
	icon := "🔋"
	if chg {
		status, icon = "Charging", "⚡"
	}
	// Present the preserved last-known reading (or the immediate
	// reading if not preserving) to the tray/UI while verification
	// occurs. Use `displayLevel` to ensure we show the intended value.
	displayLevel := batteryLvl
	batteryText = fmt.Sprintf("%s %d%% (%s)", icon, displayLevel, status)
	updateTrayTooltip(fmt.Sprintf("Battery: %d%%", displayLevel))
	updateTrayIcon(displayLevel, isCharging, preserveLastKnown)

	if logger != nil {
		logger.Printf("[CONNECT] finishConnect path=%s lvl=%d chg=%v", path, lvl, chg)
	}
	// Record the most recent successful read so quick probes can use it as
	// a trusted reference during device-change verification windows.
	if batteryLvl > 0 {
		lastKnownMu.Lock()
		lastKnownLevel = batteryLvl
		lastKnownCharging = isCharging
		lastKnownMu.Unlock()
		atomic.StoreInt64(&lastGoodReadUnix, time.Now().UnixNano())
	}
	// Immediately inform any connected UIs about the new connection.
	// If the immediate reported level is zero we enable a short-lived
	// verification window so the UI shows 'Reading…' and we can avoid
	// flashing a transient 0%. If we already have a positive level,
	// don't set the verification window — present the live level.
	readingFlag := false
	if lvl == 0 || preserveLastKnown {
		// Enable a short verification window when we have a zero or a
		// suspiciously low immediate reading so the UI shows a muted
		// last-known value instead of immediately accepting a likely
		// bogus reading.
		setReading(4 * time.Second)
		readingFlag = true
	} else {
		clearReading()
	}
	bcast := map[string]interface{}{
		"status":          "connected",
		"mode":            status,
		"statusText":      status,
		"level":           batteryLvl,
		"charging":        isCharging,
		"lastChargeTime":  lastChargeTime,
		"lastChargeLevel": lastChargeLevel,
		"deviceModel":     deviceModel,
		"updateAvailable": updateAvailable,
		"updateVersion":   updateVersion,
		"reading":         readingFlag,
	}
	if preserveLastKnown {
		bcast["lastKnown"] = true
	}
	broadcast(bcast)

	// Quick worker-backed confirmation: some receivers only reveal the
	// charging flag via alternate probing strategies. Ask the helper
	// worker for a quick probe and update the UI if it reports a
	// different (and plausible) charging state or level.
	goSafe("confirmChargingViaWorker:"+path, func() {
		if err := StartProbeWorker(); err != nil {
			return
		}
		if w := getProbeWorker(); w != nil {
			// Allow the helper to perform a thorough, non-intrusive probe
			// and accept its finding as authoritative for charging state.
			if wlvl, wchg, ok, wrid, wlen, werr := w.ProbePathAll(path); werr == nil && ok {
				// Apply any meaningful differences reported by the worker.
				if wlvl >= 0 && (wlvl != batteryLvl || wchg != isCharging) {
					lastKnownMu.Lock()
					showLastKnown = false
					lastKnownLevel = wlvl
					lastKnownCharging = wchg
					lastKnownMu.Unlock()
					batteryLvl = wlvl
					isCharging = wchg
					selectedReportID = wrid
					selectedReportLen = wlen
					useGetOnly = true
					useInputReports = false
					updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl))
					updateTrayIcon(wlvl, wchg, false)
					if logger != nil {
						logger.Printf("[CONNECT] worker-confirmed level=%d chg=%v (updated UI)", wlvl, wchg)
					}
					broadcast(map[string]interface{}{"status": "connected", "reading": false, "level": batteryLvl, "charging": isCharging})
				}
			}
		}
	})

	// Post-connect verification: perform several quick reads to confirm
	// the reported battery level and avoid flashing a single spurious 0%.
	go func() {
		attempts := 10
		zeroStreak := 0
		// confirmation counter for suspicious low reads
		confirmCount := 0
		for i := 0; i < attempts; i++ {
			time.Sleep(200 * time.Millisecond)
			// If we don't have a local device handle, don't abort immediately
			// — prefer to let the helper worker confirm the reading when a
			// stateless quick probe was used (device==nil) or when adoption
			// is in progress. Only abort when there is no worker and no
			// active force-live window.
			if device == nil && !isWorkerManagedDevice() {
				if w := getProbeWorker(); w != nil {
					if logger != nil {
						logger.Printf("[CONNECT] device==nil; attempting quick worker confirm for %s", path)
					}
					if wlvl, wchg, wok, wrid, wlen, werr := w.ProbePathAll(path); werr == nil && wok {
						// Accept worker probe as a verified live reading.
						lastKnownMu.Lock()
						showLastKnown = false
						lastKnownLevel = wlvl
						lastKnownCharging = wchg
						lastKnownMu.Unlock()
						selectedReportID = wrid
						selectedReportLen = wlen
						useGetOnly = true
						useInputReports = false
						batteryLvl = wlvl
						isCharging = wchg
						updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl))
						updateTrayIcon(wlvl, wchg, false)
						if logger != nil {
							logger.Printf("[CONNECT] quick worker confirm succeeded lvl=%d chg=%v (path=%s)", wlvl, wchg, path)
						}
						clearReading()
						broadcast(map[string]interface{}{
							"status":          "connected",
							"mode":            map[bool]string{true: "Charging", false: "Discharging"}[wchg],
							"statusText":      map[bool]string{true: "Charging", false: "Discharging"}[wchg],
							"level":           batteryLvl,
							"charging":        isCharging,
							"lastChargeTime":  lastChargeTime,
							"lastChargeLevel": lastChargeLevel,
							"deviceModel":     deviceModel,
							"updateAvailable": updateAvailable,
							"updateVersion":   updateVersion,
							"reading":         false,
						})
						// Kick off background adoption in case a persistent
						// worker-managed session can be established.
						goSafe("background_adopt_postconnect:"+path, func() {
							if lvl3, chg3, adotOk := adoptWorkerManagedPath(&hid.DeviceInfo{Path: path}); adotOk {
								setWorkerManagedDevice(true)
								if lvl3 > 0 {
									lastKnownMu.Lock()
									lastKnownLevel = lvl3
									lastKnownCharging = chg3
									lastKnownMu.Unlock()
								}
							}
						})
						return
					} else {
						if !isForceLive() {
							if logger != nil {
								logger.Printf("[CONNECT] device became nil and quick worker confirm failed: %v; aborting post-connect reads", werr)
							}
							return
						}
						// otherwise we're in a short grace window (force-live)
						// — allow verification loop to continue and wait for
						// background adoption/confirm to complete.
					}
				} else {
					if !isForceLive() {
						if logger != nil {
							logger.Printf("[CONNECT] aborting post-connect reads; device became nil (no worker)")
						}
						return
					}
					// else allow the loop to continue while in the force-live window
				}
			}
			if lvl2, chg2 := readBattery(); lvl2 >= 0 {
				// If we preserved last-known because the immediate read was
				// suspiciously low, require a couple of confirmations of the
				// low value before accepting it. Otherwise keep existing
				// zeroStreak/worker fallback behavior.
				if preserveLastKnown {
					if absInt(lvl2-lvl) <= 3 {
						confirmCount++
						if logger != nil {
							logger.Printf("[CONNECT] confirmation for suspicious reading (confirm=%d) lvl2=%d target=%d", confirmCount, lvl2, lvl)
						}
						if confirmCount >= 2 {
							// Confirmed — accept the new low reading
							batteryLvl = lvl2
							isCharging = chg2
							lastKnownMu.Lock()
							showLastKnown = false
							lastKnownMu.Unlock()
							updateTrayTooltip(fmt.Sprintf("Battery: %d%%", batteryLvl))
							updateTrayIcon(batteryLvl, isCharging, false)
							if logger != nil {
								logger.Printf("[CONNECT] accepted confirmed low reading lvl=%d chg=%v", lvl2, chg2)
							}
							clearReading()
							broadcast(map[string]interface{}{"status": "connected", "reading": false, "level": batteryLvl, "charging": isCharging})
							return
						}
						continue
					}
					// Received a non-matching reading — reset confirmation
					confirmCount = 0
					// Fall through and allow other checks to run (zeros/worker adoption)
				}
				if lvl2 == 0 && batteryLvl > 0 {
					zeroStreak++
					if logger != nil {
						logger.Printf("[CONNECT] suspicious zero reading (streak=%d) — will retry", zeroStreak)
					}
					if zeroStreak < 2 {
						// try again after a small stimulation attempt
						if !settings.NonIntrusiveMode {
							_ = sendBatteryCommandWithReportID(device, selectedReportID)
						}
						continue
					}
					// zeroStreak >= 2 — try a worker-based probe fallback
					if err := StartProbeWorker(); err == nil && probeWorker != nil {
						if logger != nil {
							logger.Printf("[CONNECT] attempting worker fallback after %d zero reads on %s", zeroStreak, currentHIDPath)
						}
						if w := getProbeWorker(); w != nil {
							if wlvl, wchg, wok, wrid, wlen, werr := w.ProbePathAll(currentHIDPath); werr == nil && wok {
								// Accept worker probe as a verified live reading — clear any
								// persisted "last-known" presentation so clients treat this
								// as a live value rather than a cached one.
								lastKnownMu.Lock()
								showLastKnown = false
								lastKnownLevel = wlvl
								lastKnownCharging = wchg
								lastKnownMu.Unlock()
								batteryLvl = wlvl
								isCharging = wchg
								updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl))
								updateTrayIcon(wlvl, wchg, false)
								if logger != nil {
									logger.Printf("[CONNECT] worker fallback probe succeeded: lvl=%d chg=%v rid=0x%02x len=%d", wlvl, wchg, wrid, wlen)
								}
								clearReading()
								broadcast(map[string]interface{}{
									"status":          "connected",
									"mode":            map[bool]string{true: "Charging", false: "Discharging"}[wchg],
									"statusText":      map[bool]string{true: "Charging", false: "Discharging"}[wchg],
									"level":           batteryLvl,
									"charging":        isCharging,
									"lastChargeTime":  lastChargeTime,
									"lastChargeLevel": lastChargeLevel,
									"deviceModel":     deviceModel,
									"updateAvailable": updateAvailable,
									"updateVersion":   updateVersion,
									"reading":         false,
								})
								return
							} else {
								if logger != nil {
									logger.Printf("[CONNECT] worker fallback probe failed (after zeros): %v", werr)
								}
							}
						}
					}
				}
				// Try worker session adoption as a last-resort before accepting a 0%.
				if wlvl2, wchg2, wok2 := adoptWorkerManagedPath(&hid.DeviceInfo{Path: currentHIDPath}); wok2 {
					useInputReports = true
					useGetOnly = true
					// Worker adoption succeeded — treat this as a verified live
					// reading and clear any persisted last-known presentation.
					lastKnownMu.Lock()
					showLastKnown = false
					lastKnownLevel = wlvl2
					lastKnownCharging = wchg2
					lastKnownMu.Unlock()
					batteryLvl = wlvl2
					isCharging = wchg2
					updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl2))
					updateTrayIcon(wlvl2, wchg2, false)
					if logger != nil {
						logger.Printf("[CONNECT] worker session adoption succeeded: lvl=%d chg=%v", wlvl2, wchg2)
					}
					clearReading()
					broadcast(map[string]interface{}{
						"status":          "connected",
						"mode":            map[bool]string{true: "Charging", false: "Discharging"}[wchg2],
						"statusText":      map[bool]string{true: "Charging", false: "Discharging"}[wchg2],
						"level":           batteryLvl,
						"charging":        isCharging,
						"lastChargeTime":  lastChargeTime,
						"lastChargeLevel": lastChargeLevel,
						"deviceModel":     deviceModel,
						"updateAvailable": updateAvailable,
						"updateVersion":   updateVersion,
						"reading":         false,
					})
					setWorkerManagedDevice(true)
					return
				}
				// Fresh successful read after connect — accept as live and
				// clear any persisted last-known presentation.
				lastKnownMu.Lock()
				showLastKnown = false
				lastKnownLevel = lvl2
				lastKnownCharging = chg2
				lastKnownMu.Unlock()
				batteryLvl = lvl2
				isCharging = chg2
				updateTrayTooltip(fmt.Sprintf("Battery: %d%%", lvl2))
				updateTrayIcon(lvl2, chg2, false)
				if logger != nil {
					logger.Printf("[CONNECT] fresh read after connect (attempt %d/%d): lvl=%d chg=%v", i+1, attempts, lvl2, chg2)
				}
				clearReading()
				broadcast(map[string]interface{}{"status": "connected", "reading": false})
				return
			}
		}
		// If we didn't obtain a valid local reading, and we had at
		// least one suspicious zero, attempt a worker-based probe as
		// a fallback so an isolated helper process can try alternate
		// probing strategies.
		if zeroStreak >= 1 {
			if err := StartProbeWorker(); err == nil && probeWorker != nil {
				if logger != nil {
					logger.Printf("[CONNECT] attempting worker probe fallback on %s", currentHIDPath)
				}
				wlvl, wchg, wok, wrid, wlen, werr := probeWorker.ProbePathAll(currentHIDPath)
				if werr != nil && strings.Contains(strings.ToLower(werr.Error()), "timeout") {
					if logger != nil {
						logger.Printf("[CONNECT] worker probe timed out, retrying once")
					}
					time.Sleep(300 * time.Millisecond)
					wlvl, wchg, wok, wrid, wlen, werr = probeWorker.ProbePathAll(currentHIDPath)
				}
				if werr == nil && wok {
					// Accept the worker fallback as a verified live reading.
					lastKnownMu.Lock()
					showLastKnown = false
					lastKnownLevel = wlvl
					lastKnownCharging = wchg
					lastKnownMu.Unlock()
					selectedReportID = wrid
					selectedReportLen = wlen
					useGetOnly = true
					useInputReports = false
					batteryLvl = wlvl
					isCharging = wchg
					updateTrayTooltip(fmt.Sprintf("Battery: %d%%", wlvl))
					updateTrayIcon(wlvl, wchg, false)
					if logger != nil {
						logger.Printf("[CONNECT] worker fallback probe succeeded: lvl=%d chg=%v rid=0x%02x len=%d", wlvl, wchg, wrid, wlen)
					}
					clearReading()
					broadcast(map[string]interface{}{"status": "connected", "reading": false})
					return
				} else {
					if logger != nil {
						logger.Printf("[CONNECT] worker fallback probe failed: %v", werr)
					}
				}
			}
		}
		if logger != nil {
			logger.Printf("[CONNECT] post-connect read attempts exhausted; will rely on periodic updates")
		}
		lastKnownMu.Lock()
		lk := lastKnownLevel
		lkchg := lastKnownCharging
		lastKnownMu.Unlock()
		if lk > 0 {
			batteryLvl = lk
			isCharging = lkchg
			updateTrayTooltip(fmt.Sprintf("Battery: %d%%", lk))
			updateTrayIcon(lk, lkchg, true)
			clearReading()
			broadcast(map[string]interface{}{"status": "connected", "reading": false, "lastKnown": true})
			return
		}
	}()
}
