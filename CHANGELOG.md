# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [1.3.0]

### Added
- Low battery notifications with configurable thresholds
- Windows native toast notifications for battery alerts
- Critical battery threshold setting (default 10%)
- Low battery threshold setting (default 20%)
- Toggle to enable/disable notifications in settings
- One notification per threshold per discharge cycle
- Notifications reset when charging

### Changed
- Notifications disabled by default (can be enabled in settings)

## [1.2.0] - TBD

### Added
- Settings page with gear icon in top-right corner
- Start with Windows toggle (Windows registry integration)
- Start Minimized option to launch directly to system tray
- Configurable refresh interval (1-60 seconds)
- Custom toast notifications for user feedback
- Settings persistence across app restarts

### Fixed
- Quit button now works with single click (TPM_RETURNCMD implementation)
- Settings apply immediately without requiring restart

### Changed
- Replaced browser alerts with custom toast notifications
- Improved UI animations and transitions

## [1.1.0] - 2024

### Added
- Persistent storage for last charge data
- Mouse model detection and display
- Last charged tracking (time and level)
- Charge history survives app restarts

### Changed
- SVG icons instead of emojis for cleaner UI

### Fixed
- Window now properly hides to system tray instead of closing

## [1.0.0] - 2024

### Added
- Initial release
- Real-time battery monitoring for Glorious wireless mice
- System tray integration
- WebView2-based modern dark UI
- Auto-reconnect on device plug/unplug
- Charging status detection
- Support for all Glorious wireless mouse models
