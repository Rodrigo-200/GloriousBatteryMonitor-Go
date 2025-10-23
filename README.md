# Glorious Battery Monitor

A Windows system tray application for monitoring battery levels of Glorious wireless mice.

## Features

- **Real-time Battery Monitoring**: Displays current battery level and charging status
- **System Tray Integration**: Runs quietly in the background with tray icon
- **Time Remaining Estimation**: Shows estimated time until empty/full (like mobile phones)
- **Last Known State**: Remembers last battery level when device disconnects
- **Charge History**: Tracks when device was last charged and to what level
- **Auto-start Support**: Option to launch with Windows
- **Update Notifications**: Checks for new versions automatically
- **HID Device Scanner**: Developer tools for diagnosing connection issues

## Building

### Prerequisites
- Go 1.19 or later
- Windows OS

### Build Commands

**Standard build (with console window):**
```bash
go build -o GloriousBatteryMonitor.exe
```

**Production build (no console window):**
```bash
go build -ldflags="-H windowsgui" -o GloriousBatteryMonitor.exe
```

## Architecture

### Core Files

- **main.go**: Application entry point, HTTP server, WebView UI, tray icon management
- **driver.go**: HID device communication and battery reading logic
- **storage.go**: Persistent data storage (charge history, settings, rate tracking)
- **logging.go**: Debug logging and HID device enumeration
- **worker.go**: Helper process for safe HID operations
- **ui.html**: Embedded WebView UI

### Key Components

#### Battery Monitoring (`reconnect()` in driver.go)
- Polls device at configured interval (default 5 seconds)
- Reads battery level and charging status via HID feature reports
- Tracks discharge/charge rates for time estimation
- Handles device disconnection gracefully

#### Rate Tracking
- Monitors battery level changes (1% minimum threshold)
- Calculates discharge/charge rates using exponential moving average
- Maintains 10-sample history window
- Persists rates across restarts (up to 7 days)

#### Time Estimation
- Requires minimum 0.5%/hour rate and valid battery level
- Shows time remaining when discharging
- Shows time to full when charging
- Uses median smoothing (3 samples) in UI to prevent flickering

#### Worker Process
- Spawned as separate process (`--hid-worker` flag)
- Performs potentially-blocking HID operations safely
- Communicates via JSON-RPC over stdin/stdout
- Prevents main process crashes from driver issues

#### UI System
- WebView2-based interface
- Server-Sent Events (SSE) for real-time updates
- Fallback polling every 3 seconds
- Caches last valid time estimate to prevent flickering

## Data Storage

All data stored in: `%APPDATA%\GloriousBatteryMonitor\`

- **charge_data.json**: Battery history and rate tracking
- **settings.json**: User preferences
- **conn_profile.json**: Device connection profiles
- **debug.log**: Application logs

## Settings

- **Start with Windows**: Auto-launch on login
- **Start Minimized**: Launch to tray without showing window
- **Refresh Interval**: How often to poll device (1-60 seconds)
- **Non-intrusive Mode**: Read-only operations (no writes)
- **Notifications**: Low/critical battery alerts
- **Battery Thresholds**: Customize warning levels

## Supported Devices

Currently supports Glorious mice with VID `0x258a`:
- Model D Wireless (PID `0x2023`)
- Model D Wired (PID `0x2012`)

Additional devices can be added to `knownDevices` map in driver.go.

## Troubleshooting

### Device Not Detected
1. Open Settings → Developer Tools
2. Click "Scan HID Devices"
3. Check if your device appears in the list
4. Verify VID/PID matches supported devices

### Inaccurate Time Estimates
- Requires at least 1% battery change to calculate rate
- Wait for 3+ samples for stable estimates
- Rates persist for 7 days; delete charge_data.json to reset

### Application Crashes
- Check `debug.log` for error messages
- Enable "Non-intrusive Mode" to avoid write operations
- Report issues with HID scan output

## Development

### Debug Mode
Set environment variables for testing:
- `GLORIOUS_NO_UI=1`: Run headless (server only)
- `GLORIOUS_NO_HID=1`: Skip HID initialization
- `GLORIOUS_FORCE_WORKER=1`: Always use worker process
- `PORT=8080`: Override web server port

### Code Structure

**Main Loop** (`updateBattery()` → `reconnect()`):
1. Attempt to connect to device
2. Read battery level and charging status
3. Update rate tracking if level changed
4. Calculate time remaining
5. Broadcast to UI clients
6. Update tray icon
7. Sleep until next interval

**Broadcast Flow**:
1. `reconnect()` calls `broadcast()` with data
2. `broadcast()` adds metadata (lastKnown, reading flags)
3. JSON payload sent to all SSE clients
4. UI updates battery gauge, status, time remaining

**Worker Communication**:
1. Main process spawns worker with `--hid-worker`
2. Worker listens on stdin for JSON commands
3. Commands: `probe`, `probe_all`, `open_session`, `get_feature_session`
4. Worker responds with JSON results on stdout
5. Worker streams input reports as events

## License

See LICENSE file for details.

## Version

Current version: 2.3.4
