//go:build windows
// +build windows

package main

import (
    "archive/zip"
    "embed"
    "encoding/hex"
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
    WM_APP_TRAY_MSG  = WM_APP + 10
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

    NIN_SELECT       = win.WM_USER + 0
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
    LastLevel       int     `json:"lastLevel"`
    LastLevelTime   string  `json:"lastLevelTime"`
    LastCharging    bool    `json:"lastCharging"`
}

type Settings struct {
    StartWithWindows         bool `json:"startWithWindows"`
    StartMinimized           bool `json:"startMinimized"`
    RefreshInterval          int  `json:"refreshInterval"`
    NotificationsEnabled     bool `json:"notificationsEnabled"`
    NonIntrusiveMode         bool `json:"nonIntrusiveMode"`
    PreferWorkerForWireless  bool `json:"preferWorkerForWireless"`
    LowBatteryThreshold      int  `json:"lowBatteryThreshold"`
    CriticalBatteryThreshold int  `json:"criticalBatteryThreshold"`
    SafeMode                 bool `json:"safeMode"`
    ShowPercentageOnIcon     bool `json:"showPercentageOnIcon"`
}

type DiagnosticsRequest struct {
    IncludeExperimental bool `json:"includeExperimental"`
}

type DeviceDiagnostics struct {
    VID            uint16   `json:"vid"`
    PID            uint16   `json:"pid"`
    UsagePage      uint16   `json:"usagePage"`
    Usage          uint16   `json:"usage"`
    Interface      int      `json:"interface"`
    Path           string   `json:"path"`
    Manufacturer   string   `json:"manufacturer,omitempty"`
    Product        string   `json:"product,omitempty"`
    SerialNumber   string   `json:"serialNumber,omitempty"`
    ReleaseNbr     int      `json:"releaseNbr,omitempty"`
    ReportDesc     string   `json:"reportDesc"`
    InputSamples   []string `json:"inputSamples"`
    FeatureSamples []string `json:"featureSamples,omitempty"`
    IsGlorious     bool     `json:"isGlorious"`
}

type DiagnosticsBundle struct {
    Timestamp           string               `json:"timestamp"`
    AppVersion          string               `json:"appVersion"`
    OSVersion           string               `json:"osVersion"`
    ExperimentalEnabled bool                 `json:"experimentalEnabled"`
    Devices             []DeviceDiagnostics   `json:"devices"`
    Summary             string               `json:"summary"`
}

const currentVersion = "2.4.4"
const iconBucketUnset = -999

var (
    device                  *hid.Device
    deviceModel             = "Unknown"
    hwnd                    win.HWND
    webviewHwnd             win.HWND
    nid                     win.NOTIFYICONDATA
    batteryText             = "Connecting..."
    batteryLvl              int
    isCharging              bool
    wasCharging             bool
    hasPrevCharging         bool
    lastChargeTime          = "Never"
    lastChargeLevel         = 0
    user32                  = syscall.NewLazyDLL("user32.dll")
    appendMenuW             = user32.NewProc("AppendMenuW")
    showWindow              = user32.NewProc("ShowWindow")
    clients                 = make(map[chan string]bool)
    clientsMu               sync.RWMutex
    w                       webview2.WebView
    serverPort              = "8765"
    dataDir                 string
    dataFile                string
    settingsFile            string
    logFile                 string
    diagnosticsDir          string
    logger                  *log.Logger
    settings                Settings
    notifiedLow             bool
    notifiedCritical        bool
    notifiedFull            bool
    lastBatteryLevel        = -1
    lastBatteryTime         time.Time
    dischargeRate           float64
    lastChargeLevel2        = -1
    lastChargeTime2         time.Time
    lastKnownLevel          = -1
    lastKnownCharging       bool
    lastKnownMu             sync.Mutex
    showLastKnown           bool
    chargeRate              float64
    rateHistory             []float64
    chargeRateHistory       []float64
    animationFrame          int
    stopAnimation           chan bool
    updateAvailable         bool
    updateVersion           string
    updateURL               string
    selectedReportID        byte = 0x00
    selectedReportLen       int  = 65
    useGetOnly              bool
    consecutiveReadFails    int
    linkDown                bool
    probeRIDs               = []byte{0x04, 0x03, 0x02, 0x01, 0x00}
    useInputReports         bool
    inputFrames             chan []byte
    cacheFile               string
    cachedProfiles          []DeviceProfile
    softLinkDownCount       int
    currentHIDPath          string
    fileMu                  sync.Mutex
    safeForInput            bool
    inputDev                *hid.Device
    inputMu                 sync.Mutex
    recordedUnplug          bool
    dropConfirmMu           sync.Mutex
    dropConfirmActive       bool
    trayMu                  sync.Mutex
    trayOps                 = make(chan func(), 64)
    iconReap                = make(chan win.HICON, 64)
    iconCache               = make(map[string]win.HICON)
    iconCacheMu             sync.Mutex
    cachedDisconnectedIcon  win.HICON
    cachedIconMu            sync.Mutex
    readerDone              chan struct{}
    taskbarCreated          = win.RegisterWindowMessage(syscall.StringToUTF16Ptr("TaskbarCreated"))
    readingUntil            time.Time
    readingMu               sync.Mutex
    lastTrayPing            time.Time
    lastTrayPong            time.Time
    lastTrayPongMu          sync.Mutex
    lastTrayPingMu          sync.Mutex
    watchdogNoPongCount     int
    lastDevChangeUnix       int64
    devChangeScheduledInt   int32
    forceFreshProbeOnceInt  int32
    lastGoodReadUnix        int64
    forceLiveUntilInt64     int64
    forceWorkerMode         bool
    lastTrayIconBucket      = iconBucketUnset
    lastTrayIconCharging    = false
    lastTrayIconDim         = false
    lastTrayIconShowPercent = false
    lastTrayIconFrame       = -1
    lastTrayIconMu          sync.Mutex
)

func safeDefer(where string) {
    if r := recover(); r != nil {
        if logger != nil {
            logger.Printf("[RECOVER] %s: %v", where, r)
        }
    }
}

func absInt(x int) int {
    if x < 0 {
        return -x
    }
    return x
}

func minUint32(a, b uint32) uint32 {
    if a < b {
        return a
    }
    return b
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

    for _, a := range os.Args[1:] {
        if a == "--hid-worker" {
            workerMain()
            return
        }
    }

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
    diagnosticsDir = filepath.Join(dataDir, "diagnostics")
    os.MkdirAll(diagnosticsDir, 0755)

    setupLogging()
    loadChargeData()
    loadSettings()
    loadConnProfile()

    if os.Getenv("GLORIOUS_FORCE_WORKER") == "1" {
        forceWorkerMode = true
        if logger != nil {
            logger.Printf("[STARTUP] forceWorkerMode enabled via GLORIOUS_FORCE_WORKER")
        }
    }

    if gbmSafeMode := os.Getenv("GBM_SAFE_MODE"); gbmSafeMode != "" {
        if gbmSafeMode == "0" {
            settings.SafeMode = false
            if logger != nil {
                logger.Printf("[STARTUP] SafeMode DISABLED via GBM_SAFE_MODE=0")
            }
        } else if gbmSafeMode == "1" {
            settings.SafeMode = true
            if logger != nil {
                logger.Printf("[STARTUP] SafeMode ENABLED via GBM_SAFE_MODE=1")
            }
        }
    }
    if logger != nil {
        logger.Printf("[STARTUP] SafeMode is %v", settings.SafeMode)
    }

    if lastChargeLevel > 0 {
        lastKnownMu.Lock()
        lastKnownLevel = lastChargeLevel
        lastKnownCharging = false
        showLastKnown = true
        lastKnownMu.Unlock()
    }

    if settings.StartWithWindows {
        enableStartup()
    }

    go checkForUpdates()

    if p := os.Getenv("PORT"); p != "" {
        serverPort = p
    }

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
        select {}
    }

    stopAnimation = make(chan bool)
    go startWebServer()
    go startTray()
    go updateBattery()
    go animateChargingIcon()

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

    if len(cachedProfiles) == 0 {
        if logger != nil {
            logger.Printf("[STARTUP] no cached profile — running quick startup probe")
        }
        go quickRefreshOnDeviceChange()
    }

    time.Sleep(500 * time.Millisecond)

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
            win.SendMessage(webviewHwnd, win.WM_SETICON, 0, uintptr(hIcon))
        }()
    }

    oldProc := win.SetWindowLongPtr(webviewHwnd, win.GWLP_WNDPROC, syscall.NewCallback(webviewWndProc))
    win.SetWindowLongPtr(webviewHwnd, win.GWLP_USERDATA, oldProc)

    url := fmt.Sprintf("http://localhost:%s", serverPort)
    if logger != nil {
        logger.Printf("[STARTUP] webview navigating to %s", url)
    }
    w.Navigate(url)

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

func serveHTML(w http.ResponseWriter, r *http.Request) {
    data, _ := content.ReadFile("ui.html")
    w.Header().Set("Content-Type", "text/html")
    w.Write(data)
}

func startWebServer() {
    http.HandleFunc("/", serveHTML)
    http.HandleFunc("/api/status", handleStatus)
    http.HandleFunc("/events", handleSSE)
    http.HandleFunc("/api/settings", handleSettings)
    http.HandleFunc("/api/update", handleUpdate)
    http.HandleFunc("/api/rescan", handleRescan)
    http.HandleFunc("/api/resize", handleResize)
    http.HandleFunc("/api/scan-hid", handleScanHID)
    http.HandleFunc("/api/diagnostics", handleDiagnostics)

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
        lastKnownMu.Lock()
        showLK := showLastKnown
        lastKnownMu.Unlock()

        deviceMu.Lock()
        present := (device != nil || isWorkerManagedDevice())
        deviceMu.Unlock()

        initState := map[string]interface{}{
            "level":           batteryLvl,
            "charging":        isCharging,
            "status":          "disconnected",
            "statusText":      "Not Connected",
            "deviceModel":     deviceModel,
            "updateAvailable": updateAvailable,
            "updateVersion":   updateVersion,
        }
        initState["reading"] = isReading()
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

    defer func() {
        clientsMu.Lock()
        delete(clients, messageChan)
        close(messageChan)
        clientsMu.Unlock()
    }()

    flusher, _ := w.(http.Flusher)
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
    lastKnownMu.Lock()
    showLK := showLastKnown
    lastKnownMu.Unlock()

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
            }
        }(client, payload)
    }
    clientsMu.RUnlock()
}

func setReading(d time.Duration) {
    readingMu.Lock()
    readingUntil = time.Now().Add(d)
    readingMu.Unlock()
}

func clearReading() {
    readingMu.Lock()
    readingUntil = time.Time{}
    readingMu.Unlock()
}

func isReading() bool {
    readingMu.Lock()
    until := readingUntil
    readingMu.Unlock()
    if until.IsZero() {
        return false
    }
    return time.Now().Before(until)
}

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

    nid = win.NOTIFYICONDATA{}
    nid.CbSize = uint32(unsafe.Sizeof(nid))
    nid.HWnd = hwnd
    nid.UID = 1
    nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
    nid.UCallbackMessage = WM_APP_TRAY_MSG
    nid.HIcon = createBatteryIcon(0, false, false, 0)
    tip, _ := syscall.UTF16FromString("Glorious Battery")
    copy(nid.SzTip[:], tip)

    win.Shell_NotifyIcon(win.NIM_ADD, &nid)
    nid.UVersion = win.NOTIFYICON_VERSION_4
    win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)
    applyTrayTooltip("Glorious Battery")

    go func() {
        t := time.NewTicker(1500 * time.Millisecond)
        defer t.Stop()
        for range t.C {
            if hwnd != 0 {
                win.PostMessage(hwnd, WM_APP_ICON_REAP, 0, 0)
            }
        }
    }()

    go func() {
        t := time.NewTicker(5 * time.Second)
        defer t.Stop()
        for range t.C {
            if hwnd == 0 {
                continue
            }
            lastTrayPingMu.Lock()
            lastTrayPing = time.Now()
            lastTrayPingMu.Unlock()
            postMessage := user32.NewProc("PostMessageW")
            postMessage.Call(uintptr(hwnd), WM_APP_TRAY_PING, 0, 0)

            time.Sleep(2 * time.Second)

            lastTrayPongMu.Lock()
            lp := lastTrayPong
            lastTrayPongMu.Unlock()

            lastTrayPingMu.Lock()
            lq := lastTrayPing
            lastTrayPingMu.Unlock()

            if lp.Before(lq) {
                watchdogNoPongCount++
                if logger != nil {
                    logger.Printf("[TRAY_WATCHDOG] miss#%d: no pong within 2s (ping=%s lastPong=%s)", watchdogNoPongCount, lq.Format(time.RFC3339), lp.Format(time.RFC3339))
                }

                if watchdogNoPongCount == 1 {
                    if logger != nil {
                        buf := make([]byte, 1<<20)
                        n := runtime.Stack(buf, true)
                        logger.Printf("[TRAY_WATCHDOG] goroutine stack dump (%d bytes):\n%s", n, string(buf[:n]))
                    }
                }

                if watchdogNoPongCount >= 2 {
                    if logger != nil {
                        logger.Printf("[TRAY_WATCHDOG] attempting icon refresh (delete+add) to recover tray interactivity")
                    }
                    if hwnd != 0 {
                        win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
                        time.Sleep(150 * time.Millisecond)
                        win.Shell_NotifyIcon(win.NIM_ADD, &nid)
                        nid.UVersion = win.NOTIFYICON_VERSION_4
                        win.Shell_NotifyIcon(win.NIM_SETVERSION, &nid)
                    }
                }
            } else {
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
        applyTrayTooltip(batteryText)
        return 0
    }

    switch msg {
    case WM_DEVICECHANGE:
        if logger != nil {
            logger.Printf("[DEVCHANGE] WM_DEVICECHANGE wParam=0x%X lParam=0x%X", wParam, lParam)
        }
        go scheduleDebouncedReconnect()
        go func() {
            time.Sleep(120 * time.Millisecond)
            deviceMu.Lock()
            p := currentHIDPath
            hadDevice := (device != nil || workerManagedDevice)
            deviceMu.Unlock()

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
                        tooltipText := formatTrayTooltip(lk, lkchg, true, deviceModel)
                        applyTrayTooltip(tooltipText)
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
                safeCloseDevice()
                return
            }
            if logger != nil {
                logger.Printf("[DEVCHANGE] immediate check scheduled for path %s", p)
            }
            lastKnownMu.Lock()
            lk := lastKnownLevel
            lkchg := lastKnownCharging
            lastKnownMu.Unlock()
            if lk >= 0 {
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
                    tooltipText := formatTrayTooltip(lk, lkchg, true, deviceModel)
                    applyTrayTooltip(tooltipText)
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
            maxAttempts := 6
            for i := 0; i < maxAttempts; i++ {
                if findDeviceInfoByPath(p) == nil {
                    if logger != nil {
                        logger.Printf("[DEVCHANGE] immediate: path %s no longer present (attempt=%d) — forcing safeCloseDevice", p, i)
                    }
                    lastKnownMu.Lock()
                    lk := lastKnownLevel
                    lkchg := lastKnownCharging
                    lastKnownMu.Unlock()

                    if lk >= 0 {
                        trayInvoke(func() {
                            batteryLvl = lk
                            isCharging = lkchg
                            tooltipText := formatTrayTooltip(lk, lkchg, true, deviceModel)
                            applyTrayTooltip(tooltipText)
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
                        trayInvoke(func() {
                            batteryLvl = 0
                            isCharging = false
                            tooltipText := formatTrayTooltip(-1, false, false, deviceModel)
                            applyTrayTooltip(tooltipText)
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
        return 0

    case win.WM_CONTEXTMENU:
        showMenu()
        return 0

    case WM_APP_TRAY_DO:
        for {
            select {
            case fn := <-trayOps:
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

    uxtheme := syscall.NewLazyDLL("uxtheme.dll")
    setPreferredAppMode := uxtheme.NewProc("SetPreferredAppMode")
    if setPreferredAppMode.Find() == nil {
        setPreferredAppMode.Call(1)
    }

    batteryItem, _ := syscall.UTF16PtrFromString(batteryText)
    appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING|win.MF_GRAYED), 0, uintptr(unsafe.Pointer(batteryItem)))

    if updateAvailable {
        updateText := fmt.Sprintf("🚀 Update Available (v%s)", updateVersion)
        updateItem, _ := syscall.UTF16PtrFromString(updateText)
        appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_STRING), ID_UPDATE, uintptr(unsafe.Pointer(updateItem)))
    }

    appendMenuW.Call(uintptr(hMenu), uintptr(win.MF_SEPARATOR), 0, 0)

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
    postMessage.Call(uintptr(hwnd), 0, 0, 0)

    win.DestroyMenu(hMenu)

    switch cmd {
    case ID_SHOW, ID_UPDATE:
        showWindow.Call(uintptr(webviewHwnd), uintptr(win.SW_SHOW))
        win.SetForegroundWindow(webviewHwnd)
    case ID_QUIT:
        trayInvoke(func() { win.Shell_NotifyIcon(win.NIM_DELETE, &nid) })
        safeCloseDevice()
        kernel32 := syscall.NewLazyDLL("kernel32.dll")
        terminateProcess := kernel32.NewProc("TerminateProcess")
        getCurrentProcess := kernel32.NewProc("GetCurrentProcess")
        handle, _, _ := getCurrentProcess.Call()
        terminateProcess.Call(handle, 0)
    }
}

func trayInvoke(fn func()) {
    select {
    case trayOps <- fn:
    default:
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

func normalizeTrayTooltip(text string) string {
    text = strings.TrimSpace(text)
    if text == "" {
        text = "Glorious Battery Monitor"
    }

    const maxTooltipChars = 127
    runes := []rune(text)
    if len(runes) > maxTooltipChars {
        runes = runes[:maxTooltipChars]
        text = string(runes)
    }
    return text
}

func updateTrayTooltipLocked(text string) {
    batteryText = text
    tip, _ := syscall.UTF16FromString(text)

    for i := range nid.SzTip {
        nid.SzTip[i] = 0
    }
    n := len(tip)
    if n > len(nid.SzTip) {
        n = len(nid.SzTip)
    }
    copy(nid.SzTip[:n], tip[:n])

    nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
    win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)

    if logger != nil {
        logger.Printf("[TOOLTIP] Updated tooltip to: %s", text)
    }
}

func applyTrayTooltip(text string) {
    sanitized := normalizeTrayTooltip(text)
    trayMu.Lock()
    defer trayMu.Unlock()
    updateTrayTooltipLocked(sanitized)
}

func updateTrayTooltip(text string) {
    sanitized := normalizeTrayTooltip(text)
    trayInvoke(func() {
        trayMu.Lock()
        defer trayMu.Unlock()
        updateTrayTooltipLocked(sanitized)
    })
}

func formatTrayTooltip(level int, charging bool, lastKnown bool, model string) string {
    if lastKnown {
        if level >= 0 {
            return fmt.Sprintf("Last known: %d%%", level)
        }
        return "Mouse Not Found"
    }
    if model == "" || model == "Unknown" {
        model = "Glorious Mouse"
    }
    if charging {
        return fmt.Sprintf("%s — %d%% (Charging)", model, level)
    }
    return fmt.Sprintf("%s — %d%%", model, level)
}

const (
    digitPatternWidth  = 3
    digitPatternHeight = 5
)

var digitPatterns = [10][5]uint8{
    {0b111, 0b101, 0b101, 0b101, 0b111},
    {0b010, 0b110, 0b010, 0b010, 0b111},
    {0b111, 0b001, 0b111, 0b100, 0b111},
    {0b111, 0b001, 0b111, 0b001, 0b111},
    {0b101, 0b101, 0b111, 0b001, 0b001},
    {0b111, 0b100, 0b111, 0b001, 0b111},
    {0b111, 0b100, 0b111, 0b101, 0b111},
    {0b111, 0b001, 0b001, 0b001, 0b001},
    {0b111, 0b101, 0b111, 0b101, 0b111},
    {0b111, 0b101, 0b111, 0b001, 0b111},
}

func drawDigitPattern(set func(int32, int32, uint32), digit int, x, y, blockSize int32, color uint32) {
    if digit < 0 || digit > 9 || blockSize < 1 {
        return
    }
    pattern := digitPatterns[digit]
    for row := int32(0); row < digitPatternHeight; row++ {
        bits := pattern[row]
        for col := int32(0); col < digitPatternWidth; col++ {
            if (bits>>(digitPatternWidth-1-col))&1 == 1 {
                for dy := int32(0); dy < blockSize; dy++ {
                    for dx := int32(0); dx < blockSize; dx++ {
                        set(x+col*blockSize+dx, y+row*blockSize+dy, color)
                    }
                }
            }
        }
    }
}

func createBatteryIcon(level int, charging bool, dim bool, frame int) win.HICON {
    defer safeDefer("createBatteryIcon")

    getSystemMetrics := user32.NewProc("GetSystemMetrics")
    smCxIcon, _, _ := getSystemMetrics.Call(uintptr(11))
    smCyIcon, _, _ := getSystemMetrics.Call(uintptr(12))

    dpiScale := float32(1)
    if hwnd != 0 {
        getDpiForWindow := user32.NewProc("GetDpiForWindow")
        if getDpiForWindow.Find() == nil {
            if dpi, _, _ := getDpiForWindow.Call(uintptr(hwnd)); dpi != 0 {
                dpiScale = float32(dpi) / 96.0
            }
        }
    }
    if dpiScale < 1 {
        dpiScale = 1
    }

    scaleFactor := dpiScale * 2
    width := int32(float32(smCxIcon) * scaleFactor)
    height := int32(float32(smCyIcon) * scaleFactor)
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
        Height:      -height,
        Planes:      1,
        BitCount:    32,
        Compression: 0,
        RedMask:     0,
        GreenMask:   0,
        BlueMask:    0,
        AlphaMask:   0,
    }

    hdc := win.GetDC(0)
    if hdc == 0 {
        if logger != nil {
            logger.Printf("[ICON] GetDC failed")
        }
        return nid.HIcon
    }
    defer win.ReleaseDC(0, hdc)

    var pBits unsafe.Pointer
    gdi32 := syscall.NewLazyDLL("gdi32.dll")
    createDIBSection := gdi32.NewProc("CreateDIBSection")

    hBitmap, _, _ := createDIBSection.Call(
        uintptr(hdc),
        uintptr(unsafe.Pointer(&bi)),
        0,
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

    scale := float32(width) / 64.0
    if scale <= 0 {
        scale = 1
    }

    bodyLeft := int32(float32(6) * scale)
    bodyTop := int32(float32(20) * scale)
    bodyRight := int32(float32(52) * scale)
    bodyBottom := int32(float32(44) * scale)
    bodyWidth := bodyRight - bodyLeft
    bodyHeight := bodyBottom - bodyTop

    if bodyRight <= bodyLeft+4 {
        bodyRight = bodyLeft + 4
    }
    if bodyBottom <= bodyTop+4 {
        bodyBottom = bodyTop + 4
    }
    if bodyRight >= width {
        bodyRight = width - 1
    }
    if bodyBottom >= height {
        bodyBottom = height - 1
    }

    cornerRadius := int32(float32(3) * scale)
    if cornerRadius < 1 {
        cornerRadius = 1
    }

    borderWidth := int32(2 * scale)
    if borderWidth < 2 {
        borderWidth = 2
    }

    borderColor := uint32(0xFF2C2C2C)
    if dim {
        borderColor = 0xFF707070
    }
    bgColor := uint32(0xFFF5F5F5)
    if dim {
        bgColor = 0xFFD0D0D0
    }

    drawRoundedRect := func(left, top, right, bottom, radius int32, color uint32, filled bool) {
        for y := top; y <= bottom; y++ {
            for x := left; x <= right; x++ {
                if filled {
                    dx := int32(0)
                    dy := int32(0)
                    if x < left+radius && y < top+radius {
                        dx = left + radius - x
                        dy = top + radius - y
                        if dx*dx+dy*dy > radius*radius {
                            continue
                        }
                    } else if x > right-radius && y < top+radius {
                        dx = x - (right - radius)
                        dy = top + radius - y
                        if dx*dx+dy*dy > radius*radius {
                            continue
                        }
                    } else if x < left+radius && y > bottom-radius {
                        dx = left + radius - x
                        dy = y - (bottom - radius)
                        if dx*dx+dy*dy > radius*radius {
                            continue
                        }
                    } else if x > right-radius && y > bottom-radius {
                        dx = x - (right - radius)
                        dy = y - (bottom - radius)
                        if dx*dx+dy*dy > radius*radius {
                            continue
                        }
                    }
                    set(x, y, color)
                } else {
                    isEdge := false
                    if y == top || y == bottom {
                        if x >= left+radius && x <= right-radius {
                            isEdge = true
                        }
                    }
                    if x == left || x == right {
                        if y >= top+radius && y <= bottom-radius {
                            isEdge = true
                        }
                    }
                    if x >= left+radius && x <= right-radius && y >= top+radius && y <= bottom-radius {
                        if (x == left+radius || x == right-radius) && (y == top+radius || y == bottom-radius) {
                            isEdge = true
                        }
                    }
                    if x < left+radius && y < top+radius {
                        dx := left + radius - x
                        dy := top + radius - y
                        dist := dx*dx + dy*dy
                        if dist >= (radius-1)*(radius-1) && dist <= radius*radius {
                            isEdge = true
                        }
                    } else if x > right-radius && y < top+radius {
                        dx := x - (right - radius)
                        dy := top + radius - y
                        dist := dx*dx + dy*dy
                        if dist >= (radius-1)*(radius-1) && dist <= radius*radius {
                            isEdge = true
                        }
                    } else if x < left+radius && y > bottom-radius {
                        dx := left + radius - x
                        dy := y - (bottom - radius)
                        dist := dx*dx + dy*dy
                        if dist >= (radius-1)*(radius-1) && dist <= radius*radius {
                            isEdge = true
                        }
                    } else if x > right-radius && y > bottom-radius {
                        dx := x - (right - radius)
                        dy := y - (bottom - radius)
                        dist := dx*dx + dy*dy
                        if dist >= (radius-1)*(radius-1) && dist <= radius*radius {
                            isEdge = true
                        }
                    }
                    if isEdge {
                        set(x, y, color)
                    }
                }
            }
        }
    }

    drawRoundedRect(bodyLeft, bodyTop, bodyRight, bodyBottom, cornerRadius, bgColor, true)
    drawRoundedRect(bodyLeft, bodyTop, bodyRight, bodyBottom, cornerRadius, borderColor, false)

    tipWidth := int32(float32(bodyWidth) * 0.18)
    if tipWidth < int32(4*scale) {
        tipWidth = int32(4 * scale)
    }
    tipLeft := bodyRight + borderWidth - 1
    tipRight := tipLeft + tipWidth
    tipHeight := int32(float32(bodyHeight) * 0.6)
    tipTop := bodyTop + (bodyHeight-tipHeight)/2
    tipBottom := tipTop + tipHeight
    tipRadius := borderWidth / 2
    if tipRadius < 1 {
        tipRadius = 1
    }
    drawRoundedRect(tipLeft, tipTop, tipRight, tipBottom, tipRadius, borderColor, true)
    drawRoundedRect(tipLeft+1, tipTop+1, tipRight-1, tipBottom-1, tipRadius, bgColor, true)

    if level > 0 {
        displayLevel := level
        if charging {
            displayLevel = level + (frame * 10)
            if displayLevel > 100 {
                displayLevel = 100
            }
        }
        padding := borderWidth + 1
        fillLeft := bodyLeft + padding
        fillTop := bodyTop + padding
        fillRight := bodyRight - padding
        fillBottom := bodyBottom - padding
        fillWidth := fillRight - fillLeft
        if fillWidth < 1 {
            fillWidth = 1
        }
        fw := int32(float32(fillWidth) * float32(displayLevel) / 100.0)
        if fw < 0 {
            fw = 0
        }
        if fw > fillWidth {
            fw = fillWidth
        }
        if fw > 0 {
            fillRadius := cornerRadius - padding
            if fillRadius < 0 {
                fillRadius = 0
            }
            for y := fillTop; y <= fillBottom; y++ {
                progress := float32(y-fillTop) / float32(fillBottom-fillTop)
                lighten := uint32(20 * (1.0 - progress))
                r := (fillColor >> 16) & 0xFF
                g := (fillColor >> 8) & 0xFF
                b := fillColor & 0xFF
                r = minUint32(r+lighten, uint32(255))
                g = minUint32(g+lighten, uint32(255))
                b = minUint32(b+lighten, uint32(255))
                gradColor := (fillColor & 0xFF000000) | ((r & 0xFF) << 16) | ((g & 0xFF) << 8) | (b & 0xFF)
                for x := fillLeft; x < fillLeft+fw; x++ {
                    dx := int32(0)
                    dy := int32(0)
                    left, top, right, bottom := fillLeft, fillTop, fillLeft+fw-1, fillBottom
                    if x < left+fillRadius && y < top+fillRadius {
                        dx = left + fillRadius - x
                        dy = top + fillRadius - y
                        if dx*dx+dy*dy > fillRadius*fillRadius {
                            continue
                        }
                    } else if x > right-fillRadius && y < top+fillRadius {
                        dx = x - (right - fillRadius)
                        dy = top + fillRadius - y
                        if dx*dx+dy*dy > fillRadius*fillRadius {
                            continue
                        }
                    } else if x < left+fillRadius && y > bottom-fillRadius {
                        dx = left + fillRadius - x
                        dy = y - (bottom - fillRadius)
                        if dx*dx+dy*dy > fillRadius*fillRadius {
                            continue
                        }
                    } else if x > right-fillRadius && y > bottom-fillRadius {
                        dx = x - (right - fillRadius)
                        dy = y - (bottom - fillRadius)
                        if dx*dx+dy*dy > fillRadius*fillRadius {
                            continue
                        }
                    }
                    set(x, y, gradColor)
                }
            }
        }
    }

    if charging && !dim && !settings.ShowPercentageOnIcon {
        boltSize := int32(float32(8) * scale)
        if boltSize < 6 {
            boltSize = 6
        }
        centerX := bodyLeft + bodyWidth/2
        centerY := bodyTop + bodyHeight/2

        boltColor := uint32(0xFFFFFFFF)
        shadowColor := uint32(0x80000000)

        points := []struct{ x, y int32 }{
            {centerX, centerY - boltSize/2},
            {centerX - boltSize/3, centerY - boltSize/6},
            {centerX + boltSize/4, centerY - boltSize/6},
            {centerX + boltSize/4, centerY},
            {centerX, centerY + boltSize/2},
            {centerX + boltSize/3, centerY + boltSize/6},
            {centerX - boltSize/4, centerY + boltSize/6},
            {centerX - boltSize/4, centerY},
        }

        for _, p := range points {
            set(p.x+1, p.y+1, shadowColor)
            set(p.x, p.y, boltColor)
        }

        for i := 0; i < len(points); i++ {
            p1 := points[i]
            p2 := points[(i+1)%len(points)]
            dx := p2.x - p1.x
            dy := p2.y - p1.y
            steps := absInt(int(dx))
            if absInt(int(dy)) > steps {
                steps = absInt(int(dy))
            }
            if steps == 0 {
                steps = 1
            }
            for step := 0; step <= steps; step++ {
                t := float32(step) / float32(steps)
                x := p1.x + int32(float32(dx)*t)
                y := p1.y + int32(float32(dy)*t)
                set(x+1, y+1, shadowColor)
                set(x, y, boltColor)
            }
        }
    }

    showPercentOverlay := settings.ShowPercentageOnIcon
    
    if showPercentOverlay && !dim && level >= 0 && level <= 100 {
        percentText := fmt.Sprintf("%d", level)
        
        padding := borderWidth + 2
        if padding < 3 {
            padding = 3
        }
        innerWidth := bodyWidth - padding*2
        innerHeight := bodyHeight - padding*2
        if innerWidth < 8 {
            innerWidth = bodyWidth - 6
        }
        if innerHeight < 10 {
            innerHeight = bodyHeight - 6
        }

        numDigits := int32(len(percentText))
        spacing := int32(1)
        if numDigits > 1 {
            spacing = 2
        }
        
        totalUnitsWidth := numDigits*digitPatternWidth + (numDigits-1)*spacing
        block := innerWidth / totalUnitsWidth
        maxBlockHeight := innerHeight / digitPatternHeight
        if maxBlockHeight < block {
            block = maxBlockHeight
        }
        
        if block < 2 {
            block = 2
        }
        if block > 7 {
            block = 7
        }

        glyphWidth := digitPatternWidth * block
        glyphHeight := digitPatternHeight * block
        glyphSpacing := spacing * block
        totalWidth := numDigits*glyphWidth + (numDigits-1)*glyphSpacing
        
        startX := bodyLeft + padding + (innerWidth-totalWidth)/2
        startY := bodyTop + padding + (innerHeight-glyphHeight)/2

        textColor := uint32(0xFFFFFFFF)
        shadowColor := uint32(0x80000000)
        
        if !charging {
            if level >= 50 {
                textColor = 0xFFFFFFFF
            } else if level >= 20 {
                textColor = 0xFF000000
                shadowColor = 0x00000000
            } else {
                textColor = 0xFFFFFFFF
            }
        }

        for i, ch := range percentText {
            digit := int(ch - '0')
            if digit < 0 || digit > 9 {
                continue
            }
            offsetX := startX + int32(i)*(glyphWidth+glyphSpacing)
            
            if shadowColor != 0 {
                drawDigitPattern(set, digit, offsetX+1, startY+1, block, shadowColor)
            }
            drawDigitPattern(set, digit, offsetX, startY, block, textColor)
        }
        
        if logger != nil {
            logger.Printf("[ICON] Percentage overlay drawn: %d%% block=%d", level, block)
        }
    }

    hMask := win.CreateBitmap(width, height, 1, 1, nil)
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
    iconInfo.HbmMask = hMask

    hIcon := win.CreateIconIndirect(&iconInfo)
    if hIcon == 0 {
        if logger != nil {
            logger.Printf("[ICON] CreateIconIndirect failed")
        }
        win.DeleteObject(win.HGDIOBJ(hBitmap))
        win.DeleteObject(win.HGDIOBJ(hMask))
        return nid.HIcon
    }

    win.DeleteObject(win.HGDIOBJ(hBitmap))
    win.DeleteObject(win.HGDIOBJ(hMask))

    return hIcon
}

func invalidateIconCache() {
    trayMu.Lock()
    current := nid.HIcon
    trayMu.Unlock()

    iconCacheMu.Lock()
    for key, icon := range iconCache {
        if icon == 0 || icon == current {
            continue
        }
        select {
        case iconReap <- icon:
        default:
        }
        delete(iconCache, key)
    }
    iconCacheMu.Unlock()

    cachedIconMu.Lock()
    if cachedDisconnectedIcon != 0 && cachedDisconnectedIcon != current {
        select {
        case iconReap <- cachedDisconnectedIcon:
        default:
        }
        cachedDisconnectedIcon = 0
    }
    cachedIconMu.Unlock()

    lastTrayIconMu.Lock()
    lastTrayIconBucket = iconBucketUnset
    lastTrayIconFrame = -1
    lastTrayIconMu.Unlock()
}

func updateTrayIcon(level int, charging bool, dim bool) {
    showPercent := settings.ShowPercentageOnIcon
    frame := animationFrame
    bucket := -1
    if level >= 0 {
        if level > 100 {
            level = 100
        }
        if showPercent {
            bucket = level
        } else {
            bucket = level / 5
        }
    }

    lastTrayIconMu.Lock()
    needsRedraw := lastTrayIconDim != dim ||
        lastTrayIconShowPercent != showPercent ||
        lastTrayIconCharging != charging ||
        lastTrayIconBucket != bucket ||
        (charging && lastTrayIconFrame != frame)
    if needsRedraw {
        lastTrayIconDim = dim
        lastTrayIconShowPercent = showPercent
        lastTrayIconCharging = charging
        lastTrayIconBucket = bucket
        if charging {
            lastTrayIconFrame = frame
        } else {
            lastTrayIconFrame = -1
        }
    }
    lastTrayIconMu.Unlock()

    if !needsRedraw {
        return
    }

    trayInvoke(func() {
        if dim {
            cachedIconMu.Lock()
            ci := cachedDisconnectedIcon
            cachedIconMu.Unlock()

            if ci == 0 {
                newCi := createBatteryIcon(0, false, true, 0)
                if newCi != 0 {
                    cachedIconMu.Lock()
                    if cachedDisconnectedIcon == 0 {
                        cachedDisconnectedIcon = newCi
                        ci = newCi
                    } else {
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
                nid.UFlags = win.NIF_ICON
                win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
                nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
                trayMu.Unlock()

                if oldIcon != 0 && oldIcon != ci {
                    select {
                    case iconReap <- oldIcon:
                    default:
                    }
                }
                return
            }
        }

        cacheKey := fmt.Sprintf("%03d:%t:%d:%t:%t", bucket, charging, frame, showPercent, dim)
        iconCacheMu.Lock()
        cachedIcon, ok := iconCache[cacheKey]
        iconCacheMu.Unlock()
        var icon win.HICON
        if ok && cachedIcon != 0 {
            icon = cachedIcon
        } else {
            icon = createBatteryIcon(level, charging, dim, frame)
            if icon == 0 {
                return
            }
            iconCacheMu.Lock()
            iconCache[cacheKey] = icon
            iconCacheMu.Unlock()
        }

        trayMu.Lock()
        oldIcon := nid.HIcon
        nid.HIcon = icon
        nid.UFlags = win.NIF_ICON
        win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
        nid.UFlags = win.NIF_ICON | win.NIF_MESSAGE | win.NIF_TIP
        trayMu.Unlock()

        cachedIconMu.Lock()
        cached := cachedDisconnectedIcon
        cachedIconMu.Unlock()
        if oldIcon != 0 && oldIcon != icon && oldIcon != cached {
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
        prevSafe := settings.SafeMode
        prevPercentage := settings.ShowPercentageOnIcon
        settings = newSettings
        saveSettings()
        if logger != nil && prevSafe != settings.SafeMode {
            logger.Printf("[SETTINGS] SafeMode toggled to %v via UI", settings.SafeMode)
        }
        if prevPercentage != settings.ShowPercentageOnIcon {
            go func() {
                invalidateIconCache()
                dim := showLastKnown
                updateTrayIcon(batteryLvl, isCharging, dim)
            }()
        }
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
        clearBackoffsForCandidates()
        _ = StartProbeWorker()
        reconnect()
        _ = tryImmediateWorkerQuickProbe()
    }()
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]bool{"started": true})
}

func handleScanHID(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost && r.Method != http.MethodGet {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    if logger != nil {
        logger.Printf("[HTTP] HID device scan requested")
    }

    result := scanAllHIDDevices()
    logHIDScanResults(result)

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

func handleStatus(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
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
        "status":          status,
        "statusText":      statusText,
        "level":           batteryLvl,
        "charging":        isCharging,
        "reading":         isReading(),
        "lastKnown":       showLK,
        "lastChargeTime":  lastChargeTime,
        "lastChargeLevel": lastChargeLevel,
        "deviceModel":     deviceModel,
        "updateAvailable": updateAvailable,
        "updateVersion":   updateVersion,
        "path":            curPath,
    }
    _ = json.NewEncoder(w).Encode(resp)
}

func sanitizeString(s string) string {
    if len(s) == 0 {
        return ""
    }
    // Replace potential PII/sensitive data with placeholder
    sanitized := strings.ReplaceAll(s, "SN:", "SN:<REDACTED>")
    sanitized = strings.ReplaceAll(sanitized, "Serial:", "Serial:<REDACTED>")
    sanitized = strings.ReplaceAll(sanitized, "serial:", "serial:<REDACTED>")
    // Remove any remaining potential serial numbers (alphanumeric strings 8+ chars)
    words := strings.Fields(sanitized)
    var result []string
    for _, word := range words {
        if len(word) >= 8 && isAlphanumeric(word) {
            result = append(result, "<REDACTED>")
        } else {
            result = append(result, word)
        }
    }
    return strings.Join(result, " ")
}

func isAlphanumeric(s string) bool {
    for _, r := range s {
        if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
            return false
        }
    }
    return true
}

func collectDeviceDiagnostics(includeExperimental bool) ([]DeviceDiagnostics, error) {
    var devices []DeviceDiagnostics
    
    // Collect diagnostics for all Glorious devices
    for _, vid := range gloriousVendorIDs {
        err := hid.Enumerate(vid, 0, func(info *hid.DeviceInfo) error {
            if isKeyboardInterface(info) {
                return nil
            }
            
            // Skip if device doesn't match our criteria
            if !shouldCollectForDiagnostics(info) {
                return nil
            }
            
            // Additional filtering similar to existing code
            if info.UsagePage == 0x01 && info.Usage == 0x06 {
                return nil // Skip keyboards
            }
            
            dev := DeviceDiagnostics{
                VID:          info.VendorID,
                PID:          info.ProductID,
                UsagePage:    info.UsagePage,
                Usage:        info.Usage,
                Interface:    info.InterfaceNbr,
                Path:         info.Path,
                Manufacturer: sanitizeString(info.MfrStr),
                Product:      sanitizeString(info.ProductStr),
                SerialNumber: sanitizeString(info.SerialNbr),
                ReleaseNbr:   int(info.ReleaseNbr),
                IsGlorious:   isGloriousVendor(info.VendorID),
            }
            
            // Try to open device for report descriptor and samples
            if device, err := hid.OpenPath(info.Path); err == nil {
                defer device.Close()
                
                // Get report descriptor (if available)
                // Note: GetReportDescriptor might not be available in all HID library versions
                // This is a best-effort collection
                dev.ReportDesc = "Not available in this version"
                
                // Collect input report samples (read-only)
                var inputSamples []string
                buf := make([]byte, 65)
            sampleLoop:
                for i := 0; i < 5; i++ { // Collect up to 5 samples
                    // Try to read with timeout using goroutine
                    resultChan := make(chan struct {
                        n int
                        err error
                    }, 1)
                    
                    go func() {
                        n, err := device.Read(buf)
                        resultChan <- struct {
                            n int
                            err error
                        }{n, err}
                    }()
                    
                    select {
                    case result := <-resultChan:
                        if result.err == nil && result.n > 0 {
                            inputSamples = append(inputSamples, hex.EncodeToString(buf[:result.n]))
                        }
                    case <-time.After(100 * time.Millisecond):
                        // Timeout - this is expected for devices that don't send input reports continuously
                        // Break out of the sample collection loop on timeout
                        break sampleLoop
                    }
                    
                    time.Sleep(50 * time.Millisecond) // Small delay between reads
                }
                dev.InputSamples = inputSamples
                
                // Experimental: collect feature reports if requested
                if includeExperimental {
                    var featureSamples []string
                    for rid := byte(0x01); rid <= 0x0F; rid++ { // Try common report IDs
                        featureBuf := make([]byte, 65)
                        featureBuf[0] = rid
                        n, err := device.GetFeatureReport(featureBuf)
                        if err == nil && n > 0 {
                            featureSamples = append(featureSamples, hex.EncodeToString(featureBuf[:n]))
                        }
                    }
                    dev.FeatureSamples = featureSamples
                }
            }
            
            devices = append(devices, dev)
            return nil
        })
        
        if err != nil && logger != nil {
            logger.Printf("[DIAG] Error enumerating VID 0x%04X: %v", vid, err)
        }
    }
    
    return devices, nil
}

func shouldCollectForDiagnostics(info *hid.DeviceInfo) bool {
    // Include known Glorious devices and potential new devices
    if isGloriousVendor(info.VendorID) {
        return true
    }
    
    // Include devices with mouse-like usage pages
    switch info.UsagePage {
    case 0x01: // Generic Desktop
        if info.Usage == 0x02 { // Mouse
            return true
        }
    case 0x0C: // Consumer
        return true
    }
    
    return false
}

func getOSVersion() string {
    version := runtime.GOOS
    if runtime.GOOS == "windows" {
        // Try to get Windows version
        if major, minor, build := getWindowsVersion(); major > 0 {
            version = fmt.Sprintf("Windows %d.%d Build %d", major, minor, build)
        } else {
            version = "Windows (unknown version)"
        }
    }
    return version
}

func getWindowsVersion() (major, minor, build uint32) {
    // Simple Windows version detection
    version := syscall.NewLazyDLL("kernel32.dll")
    proc := version.NewProc("GetVersion")
    result, _, _ := proc.Call()
    
    major = uint32(result & 0xFF)
    minor = uint32((result >> 8) & 0xFF)
    build = uint32((result >> 16) & 0xFFFF)
    
    return
}

func createDiagnosticsBundle(devices []DeviceDiagnostics, experimental bool) DiagnosticsBundle {
    // Create summary
    var summary strings.Builder
    summary.WriteString(fmt.Sprintf("Diagnostics captured %d device(s)\n", len(devices)))
    
    gloriousCount := 0
    for _, dev := range devices {
        if dev.IsGlorious {
            gloriousCount++
        }
    }
    if gloriousCount > 0 {
        summary.WriteString(fmt.Sprintf("- %d Glorious device(s)\n", gloriousCount))
    }
    
    summary.WriteString(fmt.Sprintf("- Experimental reads: %v\n", experimental))
    summary.WriteString(fmt.Sprintf("- Safe mode: %v\n", settings.SafeMode))
    
    return DiagnosticsBundle{
        Timestamp:           time.Now().Format(time.RFC3339),
        AppVersion:          currentVersion,
        OSVersion:           getOSVersion(),
        ExperimentalEnabled: experimental,
        Devices:             devices,
        Summary:             summary.String(),
    }
}

func handleDiagnostics(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    
    if logger != nil {
        logger.Printf("[HTTP] Diagnostics capture requested")
    }
    
    var req DiagnosticsRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid request body", http.StatusBadRequest)
        return
    }
    
    // Collect device diagnostics
    devices, err := collectDeviceDiagnostics(req.IncludeExperimental)
    if err != nil {
        if logger != nil {
            logger.Printf("[DIAG] Error collecting diagnostics: %v", err)
        }
        http.Error(w, "Failed to collect diagnostics", http.StatusInternalServerError)
        return
    }
    
    // Create diagnostics bundle
    bundle := createDiagnosticsBundle(devices, req.IncludeExperimental)
    
    // Create zip file
    timestamp := time.Now().Format("20060102_150405")
    zipPath := filepath.Join(diagnosticsDir, fmt.Sprintf("glorious_diagnostics_%s.zip", timestamp))
    
    zipFile, err := os.Create(zipPath)
    if err != nil {
        if logger != nil {
            logger.Printf("[DIAG] Error creating zip file: %v", err)
        }
        http.Error(w, "Failed to create diagnostics file", http.StatusInternalServerError)
        return
    }
    defer zipFile.Close()
    
    zipWriter := zip.NewWriter(zipFile)
    defer zipWriter.Close()
    
    // Add JSON bundle
    if bundleJson, err := json.MarshalIndent(bundle, "", "  "); err == nil {
        if writer, err := zipWriter.Create("diagnostics.json"); err == nil {
            writer.Write(bundleJson)
        }
    }
    
    // Add text summary
    if writer, err := zipWriter.Create("summary.txt"); err == nil {
        writer.Write([]byte(bundle.Summary))
        writer.Write([]byte("\n\nDevice Details:\n"))
        for i, dev := range bundle.Devices {
            writer.Write([]byte(fmt.Sprintf("\nDevice %d:\n", i+1)))
            writer.Write([]byte(fmt.Sprintf("  VID: 0x%04X, PID: 0x%04X\n", dev.VID, dev.PID)))
            writer.Write([]byte(fmt.Sprintf("  UsagePage: 0x%04X, Usage: 0x%04X\n", dev.UsagePage, dev.Usage)))
            writer.Write([]byte(fmt.Sprintf("  Interface: %d\n", dev.Interface)))
            if dev.Product != "" {
                writer.Write([]byte(fmt.Sprintf("  Product: %s\n", dev.Product)))
            }
            if dev.Manufacturer != "" {
                writer.Write([]byte(fmt.Sprintf("  Manufacturer: %s\n", dev.Manufacturer)))
            }
            writer.Write([]byte(fmt.Sprintf("  Release: %d\n", dev.ReleaseNbr)))
            writer.Write([]byte(fmt.Sprintf("  IsGlorious: %v\n", dev.IsGlorious)))
            if dev.ReportDesc != "" {
                writer.Write([]byte(fmt.Sprintf("  Report Descriptor: %s\n", dev.ReportDesc)))
            }
            if len(dev.InputSamples) > 0 {
                writer.Write([]byte(fmt.Sprintf("  Input Samples: %d\n", len(dev.InputSamples))))
                for j, sample := range dev.InputSamples {
                    writer.Write([]byte(fmt.Sprintf("    Sample %d: %s\n", j+1, sample)))
                }
            }
            if len(dev.FeatureSamples) > 0 {
                writer.Write([]byte(fmt.Sprintf("  Feature Samples: %d\n", len(dev.FeatureSamples))))
                for j, sample := range dev.FeatureSamples {
                    writer.Write([]byte(fmt.Sprintf("    Feature %d: %s\n", j+1, sample)))
                }
            }
        }
    }
    
    zipWriter.Close()
    zipFile.Close()
    
    if logger != nil {
        logger.Printf("[DIAG] Diagnostics saved to: %s", zipPath)
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "success":   true,
        "zipPath":   zipPath,
        "timestamp": timestamp,
        "devices":   len(devices),
        "summary":   bundle.Summary,
    })
}

func quickRefreshOnDeviceChange() {
    go func() {
        time.Sleep(350 * time.Millisecond)
        deviceMu.Lock()
        path := currentHIDPath
        deviceMu.Unlock()
        if path != "" {
            if findDeviceInfoByPath(path) == nil {
                if logger != nil {
                    logger.Printf("[DEVCHANGE] cached currentHIDPath %s no longer enumerates — forcing candidate scan", path)
                }
                path = ""
                if tryImmediateWorkerQuickProbe() {
                    return
                }
            }
        }
        if path == "" && len(cachedProfiles) > 0 {
            path = cachedProfiles[0].Path
        }
        if path == "" {
            if getProbeWorker() == nil {
                if err := StartProbeWorker(); err != nil || getProbeWorker() == nil {
                    return
                }
            }
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
            for _, p := range candidates {
                if w := getProbeWorker(); w != nil {
                    if wlvl, wchg, wok, wrid, wlen, werr := w.ProbePathAll(p); werr == nil && wok {
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
                                logger.Printf("[DEVCHANGE] quick probe on %s returned low lvl=%d while recent=%d — deferring acceptance and verifying", p, wlvl, hold)
                            }
                            setReading(3 * time.Second)
                            trayInvoke(func() {
                                batteryLvl = hold
                                isCharging = holdchg
                                tooltipText := formatTrayTooltip(hold, holdchg, true, deviceModel)
                                applyTrayTooltip(tooltipText)
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
                                            lastKnownMu.Lock()
                                            showLastKnown = false
                                            lastKnownLevel = expected
                                            lastKnownCharging = chg
                                            lastKnownMu.Unlock()
                                            tooltipText := formatTrayTooltip(expected, chg, false, deviceModel)
                                            applyTrayTooltip(tooltipText)
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
                        clearWriteFailures(p)
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

                        status := map[bool]string{true: "Charging", false: "Discharging"}[wchg]
                        lastKnownMu.Lock()
                        showLastKnown = false
                        lastKnownLevel = wlvl
                        lastKnownCharging = wchg
                        lastKnownMu.Unlock()
                        tooltipText := formatTrayTooltip(wlvl, wchg, false, deviceModel)
                        applyTrayTooltip(tooltipText)
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
                        tooltipText := formatTrayTooltip(hold, holdchg, true, deviceModel)
                        applyTrayTooltip(tooltipText)
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
                                    lastKnownMu.Lock()
                                    showLastKnown = false
                                    lastKnownLevel = expected
                                    lastKnownCharging = chg
                                    lastKnownMu.Unlock()
                                    tooltipText := formatTrayTooltip(expected, chg, false, deviceModel)
                                    applyTrayTooltip(tooltipText)
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

                selectedReportID = wrid
                selectedReportLen = wlen
                useGetOnly = true
                useInputReports = false
                batteryLvl = wlvl
                isCharging = wchg
                clearWriteFailures(path)
                if logger != nil {
                    logger.Printf("[DEVCHANGE] quick worker probe succeeded on %s lvl=%d chg=%v", path, wlvl, wchg)
                }

                status := map[bool]string{true: "Charging", false: "Discharging"}[wchg]
                lastKnownMu.Lock()
                showLastKnown = false
                lastKnownLevel = wlvl
                lastKnownCharging = wchg
                lastKnownMu.Unlock()
                tooltipText := formatTrayTooltip(wlvl, wchg, false, deviceModel)
                applyTrayTooltip(tooltipText)
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

func scheduleDebouncedReconnect() {
    atomic.StoreInt64(&lastDevChangeUnix, time.Now().UnixNano())
    if !atomic.CompareAndSwapInt32(&devChangeScheduledInt, 0, 1) {
        return
    }

    go func() {
        for {
            time.Sleep(300 * time.Millisecond)
            since := time.Since(time.Unix(0, atomic.LoadInt64(&lastDevChangeUnix)))
            if since < 900*time.Millisecond {
                continue
            }
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

func startupShortcutPath(appName string) string {
    return filepath.Join(
        os.Getenv("APPDATA"),
        `Microsoft\Windows\Start Menu\Programs\Startup`,
        appName+".lnk",
    )
}

func createStartupShortcut(appName, exePath, args string) error {
    startupDir := filepath.Dir(startupShortcutPath(appName))
    if err := os.MkdirAll(startupDir, 0755); err != nil {
        return err
    }

    linkPath := startupShortcutPath(appName)

    if err := ole.CoInitialize(0); err != nil {
        return fmt.Errorf("CoInitialize failed: %v", err)
    }
    defer ole.CoUninitialize()

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

    scV, err := oleutil.CallMethod(shellDisp, "CreateShortcut", linkPath)
    if err != nil {
        return fmt.Errorf("CreateShortcut failed: %v", err)
    }
    sc := scV.ToIDispatch()
    defer sc.Release()

    if _, err = oleutil.PutProperty(sc, "TargetPath", exePath); err != nil {
        return fmt.Errorf("Set TargetPath failed: %v", err)
    }
    if strings.TrimSpace(args) != "" {
        if _, err = oleutil.PutProperty(sc, "Arguments", args); err != nil {
            return fmt.Errorf("Set Arguments failed: %v", err)
        }
    }
    _, _ = oleutil.PutProperty(sc, "Description", appName)
    _, _ = oleutil.PutProperty(sc, "IconLocation", exePath)
    _, _ = oleutil.PutProperty(sc, "WindowStyle", 1)

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

func enableStartup() {
    exePath, err := os.Executable()
    if err != nil {
        return
    }
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
    time.Sleep(5 * time.Second)

    resp, err := http.Get("https://api.github.com/repos/Rodrigo-200/GloriousBatteryMonitor-Go/releases/latest ")
    if err != nil {
        return
    }
    defer resp.Body.Close()

    var release GitHubRelease
    if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
        return
    }

    latestVersion := release.TagName
    if len(latestVersion) > 0 && latestVersion[0] == 'v' {
        latestVersion = latestVersion[1:]
    }

    if latestVersion != currentVersion {
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

    message := fmt.Sprintf("Version %s is available. Open the app to update.", version)
    sendNotification("Update Available", message, false)

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

    oldFile := exePath + ".old"
    os.Remove(oldFile)
    if err := os.Rename(exePath, oldFile); err != nil {
        return err
    }
    if err := os.Rename(tempFile, exePath); err != nil {
        os.Rename(oldFile, exePath)
        return err
    }

    kernel32 := syscall.NewLazyDLL("kernel32.dll")
    shell32 := syscall.NewLazyDLL("shell32.dll")
    shellExecute := shell32.NewProc("ShellExecuteW")
    exePathW, _ := syscall.UTF16PtrFromString(exePath)
    verb, _ := syscall.UTF16PtrFromString("open")
    shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exePathW)), 0, 0, 1)

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
    cancelDelayedReadOnlyLocalFallback(path)
    cancelScheduledForceClose(path)
    saveConnProfile(DeviceProfile{
        Path:            path,
        ReportID:        selectedReportID,
        ReportLen:       selectedReportLen,
        UseGetOnly:      useGetOnly,
        UseInputReports: useInputReports,
    })
    preserveLastKnown := false
    lastKnownMu.Lock()
    lk := lastKnownLevel
    lastKnownMu.Unlock()
    if lvl == 0 && lk > 0 {
        batteryLvl = lk
        isCharging = chg
        preserveLastKnown = true
        lastKnownMu.Lock()
        showLastKnown = true
        lastKnownMu.Unlock()
    } else {
        if lk > 0 && lvl > 0 {
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
    displayLevel := batteryLvl
    tooltipText := formatTrayTooltip(displayLevel, isCharging, preserveLastKnown, deviceModel)
    applyTrayTooltip(tooltipText)
    updateTrayIcon(displayLevel, isCharging, preserveLastKnown)

    if logger != nil {
        logger.Printf("[CONNECT] finishConnect path=%s lvl=%d chg=%v", path, lvl, chg)
    }
    if batteryLvl > 0 {
        lastKnownMu.Lock()
        lastKnownLevel = batteryLvl
        lastKnownCharging = isCharging
        lastKnownMu.Unlock()
        atomic.StoreInt64(&lastGoodReadUnix, time.Now().UnixNano())
    }
    readingFlag := false
    if lvl == 0 || preserveLastKnown {
        setReading(4 * time.Second)
        readingFlag = true
    } else {
        clearReading()
    }
    modeText := map[bool]string{true: "Charging", false: "Discharging"}[isCharging]
    statusText := "Connected"
    if preserveLastKnown {
        statusText = "Last known"
    }
    bcast := map[string]interface{}{
        "status":          map[bool]string{true: "connected", false: "connected"}[true],
        "mode":            modeText,
        "statusText":      statusText,
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

    goSafe("confirmChargingViaWorker:"+path, func() {
        if err := StartProbeWorker(); err != nil {
            return
        }
        if w := getProbeWorker(); w != nil {
            if wlvl, wchg, ok, wrid, wlen, werr := w.ProbePathAll(path); werr == nil && ok {
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
                    tooltipText := formatTrayTooltip(wlvl, wchg, false, deviceModel)
                    applyTrayTooltip(tooltipText)
                    updateTrayIcon(wlvl, wchg, false)
                    if logger != nil {
                        logger.Printf("[CONNECT] worker-confirmed level=%d chg=%v (updated UI)", wlvl, wchg)
                    }
                    broadcast(map[string]interface{}{"status": "connected", "reading": false, "level": batteryLvl, "charging": isCharging})
                }
            }
        }
    })

    go func() {
        attempts := 10
        zeroStreak := 0
        confirmCount := 0
        for i := 0; i < attempts; i++ {
            time.Sleep(200 * time.Millisecond)
            if device == nil && !isWorkerManagedDevice() {
                if w := getProbeWorker(); w != nil {
                    if logger != nil {
                        logger.Printf("[CONNECT] device==nil; attempting quick worker confirm for %s", path)
                    }
                    if wlvl, wchg, wok, wrid, wlen, werr := w.ProbePathAll(path); werr == nil && wok {
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
                        tooltipText := formatTrayTooltip(wlvl, wchg, false, deviceModel)
                        applyTrayTooltip(tooltipText)
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
                    }
                } else {
                    if !isForceLive() {
                        if logger != nil {
                            logger.Printf("[CONNECT] aborting post-connect reads; device became nil (no worker)")
                        }
                        return
                    }
                }
            }
            if lvl2, chg2 := readBattery(); lvl2 >= 0 {
                if preserveLastKnown {
                    if absInt(lvl2-lvl) <= 3 {
                        confirmCount++
                        if logger != nil {
                            logger.Printf("[CONNECT] confirmation for suspicious reading (confirm=%d) lvl2=%d target=%d", confirmCount, lvl2, lvl)
                        }
                        if confirmCount >= 2 {
                            batteryLvl = lvl2
                            isCharging = chg2
                            lastKnownMu.Lock()
                            showLastKnown = false
                            lastKnownMu.Unlock()
                            tooltipText := formatTrayTooltip(batteryLvl, isCharging, false, deviceModel)
                            applyTrayTooltip(tooltipText)
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
                    confirmCount = 0
                }
                if lvl2 == 0 && batteryLvl > 0 {
                    zeroStreak++
                    if logger != nil {
                        logger.Printf("[CONNECT] suspicious zero reading (streak=%d) — will retry", zeroStreak)
                    }
                    if zeroStreak < 2 {
                        if !settings.NonIntrusiveMode {
                            _ = sendBatteryCommandWithReportID(device, selectedReportID)
                        }
                        continue
                    }
                    if err := StartProbeWorker(); err == nil && probeWorker != nil {
                        if logger != nil {
                            logger.Printf("[CONNECT] attempting worker fallback after %d zero reads on %s", zeroStreak, currentHIDPath)
                        }
                        if w := getProbeWorker(); w != nil {
                            wlvl, wchg, wok, wrid, wlen, werr := probeWorker.ProbePathAll(currentHIDPath)
                            if werr != nil && strings.Contains(strings.ToLower(werr.Error()), "timeout") {
                                if logger != nil {
                                    logger.Printf("[CONNECT] worker probe timed out, retrying once")
                                }
                                time.Sleep(300 * time.Millisecond)
                                wlvl, wchg, wok, wrid, wlen, werr = probeWorker.ProbePathAll(currentHIDPath)
                            }
                            if werr == nil && wok {
                                lastKnownMu.Lock()
                                showLastKnown = false
                                lastKnownLevel = wlvl
                                lastKnownCharging = wchg
                                lastKnownMu.Unlock()
                                batteryLvl = wlvl
                                isCharging = wchg
                                tooltipText := formatTrayTooltip(wlvl, wchg, false, deviceModel)
                                applyTrayTooltip(tooltipText)
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
                }
                if wlvl2, wchg2, wok2 := adoptWorkerManagedPath(&hid.DeviceInfo{Path: currentHIDPath}); wok2 {
                    useInputReports = true
                    useGetOnly = true
                    lastKnownMu.Lock()
                    showLastKnown = false
                    lastKnownLevel = wlvl2
                    lastKnownCharging = wchg2
                    lastKnownMu.Unlock()
                    batteryLvl = wlvl2
                    isCharging = wchg2
                    tooltipText := formatTrayTooltip(wlvl2, wchg2, false, deviceModel)
                    applyTrayTooltip(tooltipText)
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
                lastKnownMu.Lock()
                showLastKnown = false
                lastKnownLevel = lvl2
                lastKnownCharging = chg2
                lastKnownMu.Unlock()
                batteryLvl = lvl2
                isCharging = chg2
                tooltipText := formatTrayTooltip(lvl2, chg2, false, deviceModel)
                applyTrayTooltip(tooltipText)
                updateTrayIcon(lvl2, chg2, false)
                if logger != nil {
                    logger.Printf("[CONNECT] fresh read after connect (attempt %d/%d): lvl=%d chg=%v", i+1, attempts, lvl2, chg2)
                }
                clearReading()
                broadcast(map[string]interface{}{"status": "connected", "reading": false})
                return
            }
        }
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
                    lastKnownMu.Lock()
                    showLastKnown = false
                    lastKnownLevel = wlvl
                    lastKnownCharging = wchg
                    lastKnownMu.Unlock()
                    batteryLvl = wlvl
                    isCharging = wchg
                    tooltipText := formatTrayTooltip(wlvl, wchg, false, deviceModel)
                    applyTrayTooltip(tooltipText)
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
            tooltipText := formatTrayTooltip(lk, lkchg, true, deviceModel)
            applyTrayTooltip(tooltipText)
            updateTrayIcon(lk, lkchg, true)
            clearReading()
            broadcast(map[string]interface{}{"status": "connected", "reading": false, "lastKnown": true})
            return
        }
    }()
}
