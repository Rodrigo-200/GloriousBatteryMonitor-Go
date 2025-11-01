//go:build windows
// +build windows

package main

import (
	"testing"
	"time"
)

func TestNotificationStateTransitions(t *testing.T) {
	// Create a test device key
	testKey := DeviceKey{
		VendorID:  0x258a,
		ProductID: 0x2018,
		Release:   0x0001,
		Wireless:  true,
	}

	// Reset state before test
	deviceNotifStateMu.Lock()
	delete(deviceNotifState, testKey)
	deviceNotifStateMu.Unlock()

	// Enable notifications for testing
	settings.NotificationsEnabled = true
	settings.LowBatteryThreshold = 20
	settings.CriticalBatteryThreshold = 10

	tests := []struct {
		name          string
		level         int
		charging      bool
		expectedState NotificationState
		description   string
	}{
		{
			name:          "Initial normal level",
			level:         50,
			charging:      false,
			expectedState: NotifStateNone,
			description:   "Starting at 50% should be in none state",
		},
		{
			name:          "Drop to low battery",
			level:         18,
			charging:      false,
			expectedState: NotifStateLow,
			description:   "Dropping to 18% should trigger low battery state",
		},
		{
			name:          "Stay in low battery",
			level:         17,
			charging:      false,
			expectedState: NotifStateLow,
			description:   "Staying at 17% should remain in low state",
		},
		{
			name:          "Drop to critical",
			level:         8,
			charging:      false,
			expectedState: NotifStateCritical,
			description:   "Dropping to 8% should trigger critical state",
		},
		{
			name:          "Start charging from critical",
			level:         8,
			charging:      true,
			expectedState: NotifStateNone,
			description:   "Starting to charge should clear critical state",
		},
		{
			name:          "Reach 100%",
			level:         100,
			charging:      true,
			expectedState: NotifStateFull,
			description:   "Reaching 100% while charging should trigger full state",
		},
		{
			name:          "Unplug at 100%",
			level:         100,
			charging:      false,
			expectedState: NotifStateNone,
			description:   "Unplugging at 100% should be in none state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate battery level check
			checkAndNotifyBatteryThresholds(testKey, tt.level, tt.charging)

			// Verify state
			state := getDeviceNotifState(testKey)
			state.mu.Lock()
			actualState := state.state
			state.mu.Unlock()

			if actualState != tt.expectedState {
				t.Errorf("%s: expected state %d, got %d", tt.description, tt.expectedState, actualState)
			}
		})

		// Small delay to ensure states are updated
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNotificationDebouncing(t *testing.T) {
	testKey := DeviceKey{
		VendorID:  0x258a,
		ProductID: 0x2018,
		Release:   0x0001,
		Wireless:  true,
	}

	// Reset state
	deviceNotifStateMu.Lock()
	delete(deviceNotifState, testKey)
	deviceNotifStateMu.Unlock()

	settings.NotificationsEnabled = true
	settings.LowBatteryThreshold = 20
	settings.CriticalBatteryThreshold = 10

	// Set a shorter cooloff for testing
	state := getDeviceNotifState(testKey)
	state.notificationCooloff = 100 * time.Millisecond

	// First notification should trigger
	checkAndNotifyBatteryThresholds(testKey, 15, false)
	state.mu.Lock()
	firstNotifTime := state.lastNotifiedTime
	state.mu.Unlock()

	if firstNotifTime.IsZero() {
		t.Error("First notification should have been sent")
	}

	// Immediate second notification should be blocked by cooloff
	time.Sleep(10 * time.Millisecond)
	checkAndNotifyBatteryThresholds(testKey, 14, false)
	state.mu.Lock()
	secondNotifTime := state.lastNotifiedTime
	state.mu.Unlock()

	if !secondNotifTime.Equal(firstNotifTime) {
		t.Error("Second notification should have been blocked by cooloff")
	}

	// After cooloff period, notification should be allowed again (if state changed)
	time.Sleep(120 * time.Millisecond)
	// Move to critical to trigger new notification
	checkAndNotifyBatteryThresholds(testKey, 8, false)
	state.mu.Lock()
	thirdNotifTime := state.lastNotifiedTime
	state.mu.Unlock()

	if thirdNotifTime.Equal(firstNotifTime) {
		t.Error("Third notification should have been sent after cooloff")
	}
}

func TestNotificationSettingsDisabled(t *testing.T) {
	testKey := DeviceKey{
		VendorID:  0x258a,
		ProductID: 0x2018,
		Release:   0x0001,
		Wireless:  true,
	}

	// Reset state
	deviceNotifStateMu.Lock()
	delete(deviceNotifState, testKey)
	deviceNotifStateMu.Unlock()

	// Disable notifications
	settings.NotificationsEnabled = false

	// Try to trigger notification
	checkAndNotifyBatteryThresholds(testKey, 5, false)

	state := getDeviceNotifState(testKey)
	state.mu.Lock()
	notifTime := state.lastNotifiedTime
	state.mu.Unlock()

	if !notifTime.IsZero() {
		t.Error("Notification should not have been sent when disabled")
	}
}

func TestNotificationChargingStateChange(t *testing.T) {
	testKey := DeviceKey{
		VendorID:  0x258a,
		ProductID: 0x2018,
		Release:   0x0001,
		Wireless:  true,
	}

	// Reset state
	deviceNotifStateMu.Lock()
	delete(deviceNotifState, testKey)
	deviceNotifStateMu.Unlock()

	settings.NotificationsEnabled = true
	settings.LowBatteryThreshold = 20

	// Start at low battery
	checkAndNotifyBatteryThresholds(testKey, 15, false)
	state := getDeviceNotifState(testKey)
	state.mu.Lock()
	stateAfterLow := state.state
	state.mu.Unlock()

	if stateAfterLow != NotifStateLow {
		t.Error("Should be in low state")
	}

	// Start charging - should clear the state
	checkAndNotifyBatteryThresholds(testKey, 15, true)
	state.mu.Lock()
	stateAfterCharging := state.state
	state.mu.Unlock()

	if stateAfterCharging != NotifStateNone {
		t.Error("Starting to charge should clear low battery state")
	}

	// Stop charging at low level - should allow new notification
	checkAndNotifyBatteryThresholds(testKey, 15, false)
	state.mu.Lock()
	stateAfterUnplug := state.state
	state.mu.Unlock()

	if stateAfterUnplug != NotifStateLow {
		t.Error("Unplugging at low level should trigger low state again")
	}
}

func TestFormatPercentage(t *testing.T) {
	tests := []struct {
		level    int
		expected string
	}{
		{0, "0%"},
		{50, "50%"},
		{100, "100%"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatPercentage(tt.level)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
