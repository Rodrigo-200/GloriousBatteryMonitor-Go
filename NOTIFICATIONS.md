# Notification System Documentation

## Overview

The Glorious Battery Monitor now includes a comprehensive notification system that alerts users about battery status changes. The system implements proper threshold detection, debouncing, and per-device state tracking to ensure reliable and non-intrusive notifications.

## Features

### Notification Types

1. **Low Battery Notification**
   - Triggered when battery level drops to or below the low battery threshold
   - Default threshold: 20%
   - Configurable in settings
   - Icon: Info
   - Only fires once when crossing the threshold

2. **Critical Battery Notification**
   - Triggered when battery level drops to or below the critical battery threshold
   - Default threshold: 10%
   - Configurable in settings
   - Icon: Warning (higher priority)
   - Only fires once when crossing the threshold

3. **Charging Complete Notification**
   - Triggered when battery reaches 100% while charging
   - Fires once per charging cycle
   - Icon: Info
   - Non-critical notification

### Key Behaviors

#### Edge Detection
Notifications only trigger when **crossing** threshold boundaries, not while staying within a threshold zone:

- **Low Battery**: Only notifies when dropping FROM above 20% TO 20% or below
- **Critical Battery**: Only notifies when dropping FROM above 10% TO 10% or below
- **Charging Complete**: Only notifies when reaching 100% FROM below 100%

#### Debouncing
- **Cooloff Period**: 5 minutes (default)
- Prevents notification spam
- After a notification is sent, the same notification type won't be sent again for the same device within the cooloff period
- Exception: State transitions reset the debounce (e.g., charging → discharging)

#### Per-Device State Tracking
- Each device (identified by VendorID, ProductID, Release, Wireless) has independent notification state
- Prevents duplicate notifications across device reconnects
- State includes:
  - Last notification state (None, Low, Critical, Full)
  - Last notified level
  - Last notification time
  - Last charging state

#### Charging State Handling
- When charging starts: Clears low/critical notification states
- When charging stops: Resets state to allow fresh notifications if battery is still low
- Allows re-notification if device is unplugged at low level, used, and drops further

#### State Recovery
- When battery level rises above thresholds (e.g., after charging), the notification cooloff is cleared
- This ensures immediate notification if battery drops to low again after recovery

## Architecture

### Files
- `notifications.go` - Core notification logic and state management
- `notifications_test.go` - Comprehensive test suite
- `main.go` - Integration points and HTTP endpoint
- `driver.go` - Battery level change detection and notification triggers

### Key Functions

#### `checkAndNotifyBatteryThresholds(key DeviceKey, level int, charging bool)`
Main entry point for checking battery thresholds and sending notifications.
- Called whenever battery level is updated
- Implements state machine logic
- Handles debouncing and edge detection

#### `getDeviceNotifState(key DeviceKey) *DeviceNotificationState`
Retrieves or creates per-device notification state.

#### `clearAllNotificationStates()`
Clears all notification states for all devices.
- Called when notification settings change
- Ensures fresh start after configuration changes

#### `resetNotificationStateForDevice(key DeviceKey)`
Resets notification state for a specific device.
- Called on device reconnect (optional)

#### `testNotifications()`
Sends test notifications for debugging.
- Accessible via `/api/test-notifications` endpoint

### State Machine

```
NotifStateNone (No notification needed)
  ↓ (level drops to ≤ Low Threshold while discharging)
NotifStateLow (Low battery notification sent)
  ↓ (level drops to ≤ Critical Threshold while discharging)
NotifStateCritical (Critical battery notification sent)
  ↓ (starts charging)
NotifStateNone
  ↓ (reaches 100% while charging)
NotifStateFull (Charging complete notification sent)
  ↓ (unplugs)
NotifStateNone
```

## Configuration

### Settings

```json
{
  "notificationsEnabled": true,
  "lowBatteryThreshold": 20,
  "criticalBatteryThreshold": 10
}
```

- **notificationsEnabled**: Master switch for all notifications
- **lowBatteryThreshold**: Battery percentage at which low battery warning is triggered (1-100)
- **criticalBatteryThreshold**: Battery percentage at which critical battery warning is triggered (1-100)

### Best Practices

1. **Thresholds**:
   - Critical threshold should be lower than low threshold
   - Recommended: Low = 20%, Critical = 10%
   - Adjust based on usage patterns

2. **Testing**:
   - Use `/api/test-notifications` endpoint to verify notification delivery
   - Check logs for `[NOTIF]` entries to debug issues

3. **State Management**:
   - Changing thresholds automatically clears all notification states
   - This prevents stale state issues after configuration changes

## Implementation Details

### Windows Notification System

The app uses Windows Shell_NotifyIcon API with balloon notifications (tray balloons).

#### Notification Properties
- **Title**: Short title (e.g., "Low Battery", "Charging Complete")
- **Message**: Detailed message including percentage
- **Icon**: NIIF_INFO (info) or NIIF_WARNING (critical)
- **Duration**: System default (~10 seconds)

#### Fallback Strategy
If Windows 10/11 toast notifications are unavailable, the system automatically falls back to tray balloon notifications, which are universally supported on Windows.

### Future Enhancements

Potential improvements for future releases:

1. **Windows Toast Notifications with AUMID**
   - Register application with AUMID (Application User Model ID)
   - Create Start Menu shortcut with AUMID
   - Enable rich toast notifications with action buttons
   - Better persistence and action center integration

2. **Configurable Debounce Period**
   - Add setting to adjust cooloff period
   - Per-notification-type cooloffs

3. **Custom Notification Sounds**
   - Allow users to select notification sounds
   - Different sounds for different severity levels

4. **Notification History**
   - Track recent notifications
   - Display in UI

5. **Battery Drain Rate Alerts**
   - Alert if battery draining faster than expected
   - Useful for detecting unusual power consumption

## Testing

### Unit Tests

Run the test suite:
```bash
go test -v notifications_test.go notifications.go
```

Tests cover:
- State transitions
- Edge detection
- Debouncing
- Charging state changes
- Settings disabled behavior
- Per-device isolation

### Manual Testing

1. Enable notifications in settings
2. Set low threshold to current level + 5%
3. Wait for battery to drop
4. Verify notification appears
5. Check logs for `[NOTIF]` entries

### Integration Testing

Use the test endpoint:
```bash
curl -X POST http://localhost:8765/api/test-notifications
```

This sends a test info notification and a test critical notification with a 2-second delay between them.

## Logging

All notification events are logged with the `[NOTIF]` prefix:

```
[NOTIF] Critical battery threshold crossed: level=8%
[NOTIF] Sent notification: title="Critical Battery" message="Battery is critically low at 8%!" critical=true
[NOTIF] Charging complete notification: level=100%
[NOTIF] Reset notification state for device 258a:2018:0001:1
[NOTIF] Cleared notification state for device 258a:2018:0001:1
```

## Troubleshooting

### Notifications Not Appearing

1. **Check Settings**
   - Ensure `notificationsEnabled` is `true`
   - Verify thresholds are set correctly
   - Check Windows notification settings

2. **Check Logs**
   - Look for `[NOTIF]` entries
   - Verify battery level changes are detected
   - Check for error messages

3. **Test Notifications**
   - Use `/api/test-notifications` endpoint
   - If test notifications work, issue is with threshold logic
   - If test notifications don't work, issue is with Windows notification system

4. **Windows Settings**
   - Verify app has notification permissions
   - Check Focus Assist settings
   - Ensure notifications are not blocked system-wide

### Duplicate Notifications

If receiving duplicate notifications:
1. Check if multiple instances of the app are running
2. Verify cooloff period is configured correctly
3. Check logs for unexpected state resets

### Notifications Not Debouncing

If receiving too many notifications:
1. Verify cooloff period is set (default: 5 minutes)
2. Check for device disconnects/reconnects (resets state)
3. Review logs for state transitions

## API Reference

### POST /api/test-notifications

Sends test notifications for debugging.

**Response:**
```json
{
  "status": "triggered"
}
```

**Behavior:**
- Sends an info notification immediately
- Sends a critical notification after 2 seconds
- Only works if notifications are enabled in settings

## License

Part of Glorious Battery Monitor - see main LICENSE file.
