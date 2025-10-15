# Changelog

All notable changes to this project will be documented in this file.

## [2.2.4] - 2024-01-XX

### Changed
- Renamed executable from `GloriousBatteryMonitor.exe` to `GloriousBatteryMonitor-Go.exe` for code signing differentiation
- Updated GitHub Actions workflow to build with new executable name
- Updated auto-update logic to look for new asset name in releases
- Updated README documentation with new executable name

## [2.2.3] - 2024-01-XX

### Changed
- Time remaining display now shows days when >= 24 hours (e.g., "4d 3h" instead of "99h")

## [2.2.2] - 2024-01-XX

### Changed
- Increased window size to 520x700 pixels
- Made window non-resizable
- Added dynamic height adjustment to 760px when update banner appears

### Added
- Clickable "🚀 Update Available" item in tray menu

## [2.2.1] - 2024-01-XX

### Fixed
- Auto-update now requires user permission instead of auto-installing
- Update mechanism replaced batch script with native Windows `MoveFileEx` API to avoid antivirus false positives

### Added
- Update banner in UI with "Install Now" button
- User-controlled update installation process

## [2.2.0] - 2024-01-XX

### Added
- Auto-update functionality with GitHub release checking
- Update notifications in system tray

## [2.1.0] - 2024-01-XX

### Added
- Time remaining estimation for battery discharge and charging
- Exponential moving average (EMA) for rate calculations
- Persistent storage of discharge and charge rates

## [2.0.0] - 2024-01-XX

### Added
- WebView2-based UI with dark theme
- Real-time battery monitoring via Server-Sent Events (SSE)
- System tray integration with custom ARGB icons
- Charging animation in tray icon
- Settings page with customizable options
- Start with Windows option
- Start minimized option
- Configurable refresh interval
- Low battery notifications (configurable thresholds)
- Critical battery notifications
- Full charge notifications
- Charge history tracking
- Support for all Glorious wireless mice models

### Technical
- Native Windows API integration for registry and system operations
- HID protocol implementation for battery reading
- Custom battery icon rendering with transparency
- Memory optimizations (~10MB RAM usage)
