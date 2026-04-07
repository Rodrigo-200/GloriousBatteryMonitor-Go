<div align="center">

# 🖱️ Glorious Battery Monitor (GBM)

**A lightweight, open-source system tray app for checking real-time battery levels of Glorious wireless mice.**

[![Version](https://img.shields.io/badge/Version-v3.2.5-brightgreen?style=flat)](../../releases)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows-0078D6?style=flat&logo=windows)](https://www.microsoft.com/windows)
[![.NET](https://img.shields.io/badge/.NET-8.0-512BD4?style=flat&logo=dotnet)](https://dotnet.microsoft.com/)

[Features](#-features) • [Installation](#-installation) • [Usage](#-usage) • [Supported Devices](#-supported-devices) • [Building](#-building-from-source)

</div>

---

## ✨ Features

- 🔋 **Live Battery Percentage** – Exact value from your Glorious mouse (no LED guessing)
- ⚡ **Charging Detection** – Shows charge state in real-time
- 🎯 **System Tray Integration** – Clean tray icon with quick controls
- 📅 **Charge History** – Tracks last charge level and time
- 🔄 **Auto Reconnect** – Detects when mouse is plugged/unplugged
- 🔁 **Auto Updates** – Ships updates silently via Velopack
- ⚙️ **Custom Alerts** – Set low and critical battery warnings
- 💾 **Lightweight** – App uses ~10MB RAM
- 🛡️ **Safe HID Mode** – Read-only probing by default with Glorious-specific allowlisting

> ⏱️ **Note:** The "time remaining" value is **only an estimation** and may not always be accurate.  
> This is a **known limitation**, and we're working to improve its accuracy in future releases.

---

## 📸 Screenshots



---

## 🚀 Installation

1. **Download:** Get the latest release from [Releases](../../releases).
2. **Run:** Double-click `GloriousBatteryMonitor.exe`.
3. **Done:** The app starts in your Windows system tray and will keep itself up to date automatically.

**Requirements**
- Windows 10/11 (64-bit)
- .NET 8 Runtime ([download here](https://dotnet.microsoft.com/en-us/download/dotnet/8.0) if not already installed)
- Glorious wireless mouse

---

## 📖 Usage

- 🖱️ **Left Click** → Show/hide main window
- ⚙️ **Right Click** → Open tray menu (Battery info, Show Window, Quit)
- ❌ **Close Window** → Minimizes to tray (use "Quit" to fully exit)

---

## 🖱️ Supported Devices

> Tested and confirmed working with **Model D/D2 Wireless**.  
> Active work is ongoing to support the **Model I2 Wireless**.

| Model | Wired | Wireless | Status |
|-------|-------|----------|:------:|
| Model O / O- | ❔ | ❔ | Untested |
| Model O2 | ❔ | ❔ | Untested |
| Model D | ✅ | ✅ | ✅ Working |
| Model D- | ❔ | ❔ | Untested |
| **Model D2** | ✅ | ✅ | ✅ Working |
| Model I2 | ✅ | ✅ | ✅ Working |
| Model O Pro | ❔ | ❔ | Untested |

**Known Vendor IDs:** `0x258A` (Glorious LLC), `0x093A` (Pixart — used by newer receivers)

<details>
<summary>View Product IDs</summary>

```
Model O:      0x2011 (Wired), 0x2013 (Wireless)
Model O-:     0x2019 (Wired), 0x2024 (Wireless)
Model O Pro:  0x2017 (Wired), 0x2018 (Wireless)
Model O2:     0x2009 (Wired), 0x200b (Wireless)
Model D:      0x2012 (Wired), 0x2023 (Wireless)
Model D-:     0x2015 (Wired), 0x2025 (Wireless)
Model D2:     0x2031 (Wired), 0x2033 (Wireless) / 0x093A:0x824D (Pixart receiver)
Model I:      0x2036 (Wired), 0x2046 (Wireless)
Model I2:     0x2014 (Wired), 0x2016 (Wireless)
```

</details>

---

## 🛠️ Building from Source

**Prerequisites**
- [.NET 8 SDK](https://dotnet.microsoft.com/en-us/download/dotnet/8.0)
- Windows 10/11
- Git

**Commands**
```bash
git clone https://github.com/Rodrigo-200/GloriousBatteryMonitor-Go.git
cd GloriousBatteryMonitor-Go
dotnet restore
dotnet build --configuration Release
```

**Running tests**
```bash
dotnet test --verbosity normal
```

**Key dependencies**
- [HidSharp](https://github.com/IntergatedCircuits/HidSharp) – HID device communication
- [Velopack](https://github.com/velopack/velopack) – Auto-update framework

---

## 🔧 Technical Overview

**Architecture**
- **Language:** C# / .NET 8
- **HID layer:** HidSharp (multi-interface enumeration)
- **UI:** Windows system tray (WinForms)
- **Updates:** Velopack (GitHub releases backend)

**HID Probe Strategy (Pixart PAW3370 receiver)**

The app uses a waterfall of probe candidates to find battery data across device interfaces:

```
Candidate D  →  GetFeature sweep on col01 (FF00 vendor interface)
Candidate F  →  GetFeature on col01 + listen for response on col05 (cross-interface pattern)
Candidate A  →  SetFeature with battery request payload on col01
Candidate B  →  SetFeature with alternate payload on col01
Candidate E  →  Passive stream read on col05 (unsolicited reports)
```

---

## 🐞 Troubleshooting

### Device not detected / shows "Disconnected"

1. Check the debug log at `%APPDATA%\GloriousBatteryMonitor\debug.log`
2. Try unplugging and re-plugging the USB receiver
3. If your mouse model shows as "In Progress" in the supported devices table above, battery reading may not yet be implemented for it — check back in a future release

### Antivirus False Positives

Some antivirus software may flag the executable.  
✅ **Solution:** Whitelist the executable or report a false positive to your AV vendor.
---

## 🙏 Acknowledgments

- [AwesomeTy18/GloriousBatteryMonitor](https://github.com/AwesomeTy18/GloriousBatteryMonitor) – Original C# reference for HID protocol research

---

## ☕ Support

If GBM is useful to you, consider buying me a coffee — it helps keep the project going. ❤️

<p align="center">
  <a href="https://www.buymeacoffee.com/gloriousbattery" target="_blank">
    <img src="https://cdn.buymeacoffee.com/buttons/v2/default-yellow.png" alt="Buy Me A Coffee" width="200"/>
  </a>
</p>

⭐ **Star this repo** if you find it useful!

</div>

