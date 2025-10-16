<div align="center">

# 🖱️ Glorious Battery Monitor (GBM)

**A lightweight, open-source system tray app for checking real-time battery levels of Glorious wireless mice.**

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows-0078D6?style=flat&logo=windows)](https://www.microsoft.com/windows)

[Features](#-features) • [Installation](#-installation) • [Usage](#-usage) • [Supported Devices](#-supported-devices) • [Building](#-building-from-source)

</div>

---

## ✨ Features

- 🔋 **Live Battery Percentage** – Exact value from your Glorious mouse (no LED guessing)
- ⚡ **Charging Detection** – Shows charge state in real-time
- 🎯 **System Tray Integration** – Clean tray icon with quick controls
- 📅 **Charge History** – Tracks last charge level and time
- 🔄 **Auto Reconnect** – Detects when mouse is plugged/unplugged
- ⚙️ **Custom Alerts** – Set low and critical battery warnings
- 💾 **Lightweight** – App uses ~10MB RAM

> ⏱️ **Note:** The “time remaining” value is **only an estimation** and may not always be accurate.  
> This is a **known limitation**, and we’re working to improve its accuracy in future releases.

---

## 📸 Screenshots

<div align="center">

### Main Interface
![Main UI](docs/main-page.png)

### Settings Page
![System Tray](docs/settings-page.png)

</div>

---

## 🚀 Installation

1. **Download:** Get the latest release from [Releases](../../releases).  
2. **Run:** Double-click `GloriousBatteryMonitor-Go.exe`.  
3. **Done:** The app starts in your Windows system tray.  

**Requirements**
- Windows 10/11 (64-bit)
- WebView2 Runtime (included on Windows 11)
- Glorious wireless mouse

---

## 📖 Usage

- 🖱️ **Left Click** → Show/hide main window  
- ⚙️ **Right Click** → Open tray menu (Battery info, Show Window, Quit)  
- ❌ **Close Window** → Minimizes to tray (use "Quit" to fully exit)

---

## 🖱️ Supported Devices

> Tested with **Model D Wireless**.  
> Other Glorious mice should work, but are not yet verified.

| Model | Wired | Wireless | Tested |
|--------|--------|-----------|:------:|
| Model O / O- | ❔ | ❔ | ❌ |
| Model O2 | ❔ | ❔ | ❌ |
| Model D | ✅ | ✅ | ✅ |
| Model D- / D2 | ❔ | ❔ | Currently testing |
| Model I / I2 | ❔ | ❔ | ❌ |
| Model O Pro | ❔ | ❔ | ❌ |

**Vendor ID:** `0x258a (Glorious LLC)`

<details>
<summary>View Product IDs</summary>

```

Model O:      0x2011 (Wired), 0x2013 (Wireless)
Model O-:     0x2019 (Wired), 0x2024 (Wireless)
Model O Pro:  0x2017 (Wired), 0x2018 (Wireless)
Model O2:     0x2009 (Wired), 0x200b (Wireless)
Model D:      0x2012 (Wired), 0x2023 (Wireless)
Model D-:     0x2015 (Wired), 0x2025 (Wireless)
Model D2:     0x2031 (Wired), 0x2033 (Wireless)
Model I:      0x2036 (Wired), 0x2046 (Wireless)
Model I2:     0x2014 (Wired), 0x2016 (Wireless)

````

</details>

---

## 🛠️ Build from Source

**Prerequisites**
- [Go 1.21+](https://go.dev/dl/)
- Windows 10/11
- Git

**Commands**
```bash
git clone https://github.com/Rodrigo-200/GloriousBatteryMonitor.git
cd GloriousBatteryMonitor
go mod download
go build -ldflags -H=windowsgui -o GloriousBatteryMonitor-Go.exe
````

**Dependencies**

- [go-webview2](https://github.com/jchv/go-webview2) - WebView2 bindings for Go
- [go-hid](https://github.com/sstallion/go-hid) - HID device communication
- [win](https://github.com/lxn/win) - Windows API bindings

---

## 🔧 Technical Overview

**Battery Protocol**

```go
Command:  {0x00, 0x00, 0x00, 0x02, 0x02, 0x00, 0x83}
Response: inputReport[6] == 0x83 (valid)
           inputReport[8] = battery level (0–100)
           inputReport[7] = charging status (1 = charging)
```

**Architecture**

* **Backend:** Go (Windows API + HID)
* **Frontend:** HTML/CSS/JS via WebView2
* **Updates:** Real-time via Server-Sent Events (SSE)
* **Tray:** Custom ARGB icons with transparency

---

## 🐞 Troubleshooting

### Antivirus False Positives

Some antivirus software may flag the app due to WebView2’s crash reporter (Crashpad).
✅ **Solution:** Whitelist the executable or report a false positive.

### Memory Usage

The app uses ~10MB RAM.
The WebView2 runtime adds 50–100MB (standard for Chromium-based UIs).

---

## 🙏 Acknowledgments

* [Cruxial0/GloriousBatteryMonitor (C#)](https://github.com/Cruxial0/GloriousBatteryMonitor) – Reference for HID protocol implementation.

---

## ☕ Support Us

If you find **Glorious Battery Monitor** helpful, you can buy me a coffee — every bit helps keep it updated. ❤️

<p align="center">
  <a href="https://www.buymeacoffee.com/gloriousbattery" target="_blank">
    <img src="https://cdn.buymeacoffee.com/buttons/v2/default-yellow.png" alt="Buy Me A Coffee" width="200"/>
  </a>
</p>

⭐ **Star this repo** if you find it useful!

</div>
