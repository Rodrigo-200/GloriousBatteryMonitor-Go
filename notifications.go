//go:build windows
// +build windows

package main

import (
	"fmt"
	"sync"
	"time"
)

// NotificationState represents the current notification state for a device
type NotificationState int

const (
	NotifStateNone NotificationState = iota
	NotifStateLow
	NotifStateCritical
	NotifStateFull
)

// DeviceNotificationState tracks notification state per device to prevent duplicate notifications
type DeviceNotificationState struct {
	mu                  sync.Mutex
	state               NotificationState
	lastNotifiedLevel   int
	lastNotifiedTime    time.Time
	lastChargingState   bool
	notificationCooloff time.Duration
}

var (
	deviceNotifState     = make(map[DeviceKey]*DeviceNotificationState)
	deviceNotifStateMu   sync.Mutex
	notificationDebounce = 5 * time.Minute // Don't spam the same notification within this period
)

// getDeviceNotifState returns or creates the notification state for a device
func getDeviceNotifState(key DeviceKey) *DeviceNotificationState {
	deviceNotifStateMu.Lock()
	defer deviceNotifStateMu.Unlock()

	state, exists := deviceNotifState[key]
	if !exists {
		state = &DeviceNotificationState{
			state:               NotifStateNone,
			lastNotifiedLevel:   -1,
			notificationCooloff: notificationDebounce,
		}
		deviceNotifState[key] = state
	}
	return state
}

// checkAndNotifyBatteryThresholds checks battery level against thresholds and sends notifications
// This implements proper edge detection - only notifies when crossing threshold boundaries
func checkAndNotifyBatteryThresholds(key DeviceKey, level int, charging bool) {
	if !settings.NotificationsEnabled {
		return
	}

	// Don't send notifications if level is invalid
	if level < 0 || level > 100 {
		return
	}

	state := getDeviceNotifState(key)
	state.mu.Lock()
	defer state.mu.Unlock()

	prevState := state.state
	prevLevel := state.lastNotifiedLevel
	prevCharging := state.lastChargingState
	now := time.Now()

	// Determine current notification state based on level and charging
	var currentState NotificationState
	if charging {
		if level >= 100 {
			currentState = NotifStateFull
		} else {
			// While charging but not full, we're in no-notification state
			// (unless we were previously in low/critical and need to clear that)
			currentState = NotifStateNone
		}
	} else {
		// Discharging
		if level <= settings.CriticalBatteryThreshold {
			currentState = NotifStateCritical
		} else if level <= settings.LowBatteryThreshold {
			currentState = NotifStateLow
		} else {
			currentState = NotifStateNone
		}
	}

	// Check if we should notify based on state transitions and cooloffs
	shouldNotify := false
	notifTitle := ""
	notifMessage := ""
	isCritical := false

	// Case 1: Charging state changed (started or stopped charging)
	if charging != prevCharging {
		// Reset the state when charging state changes
		// This allows re-notification if battery drops again after charging
		if charging {
			// Just started charging - clear any low/critical notifications
			state.state = NotifStateNone
		} else {
			// Just unplugged - reset to allow fresh notifications when discharging
			state.state = NotifStateNone
		}
		state.lastChargingState = charging
	}

	// Case 2: Crossing into a new threshold while discharging
	if !charging {
		if currentState == NotifStateCritical && prevState != NotifStateCritical {
			// Crossed into critical territory
			if state.lastNotifiedTime.IsZero() || now.Sub(state.lastNotifiedTime) > state.notificationCooloff {
				shouldNotify = true
				notifTitle = "Critical Battery"
				notifMessage = "Battery is critically low at " + formatPercentage(level) + "!"
				isCritical = true

				if logger != nil {
					logger.Printf("[NOTIF] Critical battery threshold crossed: level=%d%%", level)
				}
			}
		} else if currentState == NotifStateLow && prevState != NotifStateLow && prevState != NotifStateCritical {
			// Crossed into low battery (but not coming from critical)
			if state.lastNotifiedTime.IsZero() || now.Sub(state.lastNotifiedTime) > state.notificationCooloff {
				shouldNotify = true
				notifTitle = "Low Battery"
				notifMessage = "Battery is low at " + formatPercentage(level) + "."
				isCritical = false

				if logger != nil {
					logger.Printf("[NOTIF] Low battery threshold crossed: level=%d%%", level)
				}
			}
		} else if currentState == NotifStateNone && (prevState == NotifStateLow || prevState == NotifStateCritical) {
			// Recovered above thresholds - allow future notifications without debounce penalty
			state.lastNotifiedTime = time.Time{}
		}
	}

	// Case 3: Charging complete (reached 100%)
	if charging && currentState == NotifStateFull && prevState != NotifStateFull {
		// Just reached 100% while charging
		if state.lastNotifiedTime.IsZero() || now.Sub(state.lastNotifiedTime) > state.notificationCooloff {
			shouldNotify = true
			notifTitle = "Charging Complete"
			notifMessage = "Battery is fully charged at 100%."
			isCritical = false

			if logger != nil {
				logger.Printf("[NOTIF] Charging complete notification: level=%d%%", level)
			}
		}
	}

	// Send notification if needed
	if shouldNotify {
		go sendNotification(notifTitle, notifMessage, isCritical)
		state.lastNotifiedTime = now
		state.lastNotifiedLevel = level
		state.state = currentState

		if logger != nil {
			logger.Printf("[NOTIF] Sent notification: title=%q message=%q critical=%v", notifTitle, notifMessage, isCritical)
		}
	} else {
		// Update state even if we didn't notify (to track state transitions)
		state.state = currentState
		state.lastChargingState = charging
	}

	// Update level tracking
	if prevLevel == -1 || absInt(level-prevLevel) >= 1 {
		state.lastNotifiedLevel = level
	}
}

// resetNotificationStateForDevice resets notification state when device disconnects/reconnects
func resetNotificationStateForDevice(key DeviceKey) {
	deviceNotifStateMu.Lock()
	defer deviceNotifStateMu.Unlock()

	if state, exists := deviceNotifState[key]; exists {
		state.mu.Lock()
		state.state = NotifStateNone
		state.lastNotifiedTime = time.Time{} // Allow immediate notification on reconnect if needed
		state.mu.Unlock()

		if logger != nil {
			logger.Printf("[NOTIF] Reset notification state for device %s", key.String())
		}
	}
}

// clearAllNotificationStates clears all notification states (useful for testing or settings changes)
func clearAllNotificationStates() {
	deviceNotifStateMu.Lock()
	defer deviceNotifStateMu.Unlock()

	for key, state := range deviceNotifState {
		state.mu.Lock()
		state.state = NotifStateNone
		state.lastNotifiedTime = time.Time{}
		state.mu.Unlock()

		if logger != nil {
			logger.Printf("[NOTIF] Cleared notification state for device %s", key.String())
		}
	}
}

// formatPercentage formats a percentage value
func formatPercentage(level int) string {
	return fmt.Sprintf("%d%%", level)
}

// testNotifications triggers test notifications for debugging
func testNotifications() {
	if !settings.NotificationsEnabled {
		if logger != nil {
			logger.Printf("[NOTIF] Test notifications skipped - notifications disabled")
		}
		return
	}

	if logger != nil {
		logger.Printf("[NOTIF] Sending test notifications")
	}

	go func() {
		sendNotification("Test Notification", "This is a test notification from Glorious Battery Monitor.", false)
		time.Sleep(2 * time.Second)
		sendNotification("Test Critical Notification", "This is a test critical notification.", true)
	}()
}
