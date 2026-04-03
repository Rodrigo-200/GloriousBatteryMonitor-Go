using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class NotificationService : INotificationService
{
    private readonly ILogger<NotificationService> _logger;
    private readonly Dictionary<string, DeviceNotificationState> _deviceStates = new();
    private readonly object _lock = new();

    public event Action<NotificationType, string, string>? NotificationTriggered;

    public NotificationService(ILogger<NotificationService> logger)
    {
        _logger = logger;
    }

    public void ProcessBatteryUpdate(BatteryState current, BatteryState? previous, AppSettings settings)
    {
        if (!settings.NotificationsEnabled)
            return;

        lock (_lock)
        {
            var cooldown = TimeSpan.FromMinutes(settings.NotificationCooldownMinutes);

            try
            {
                string deviceKey = current.DeviceName;
                var state = GetOrCreateState(deviceKey);

                // Handle disconnect
                if (current.Connection == ConnectionState.NotConnected)
                {
                    if (previous != null && previous.Connection == ConnectionState.Connected)
                    {
                        TryFireNotification(state, NotificationType.Disconnected,
                            "Device Disconnected",
                            "Your Glorious mouse has been disconnected.", cooldown);
                    }

                    return;
                }

                // Only process battery notifications for connected devices
                if (current.Connection != ConnectionState.Connected)
                    return;

                // Reset notification flags on charging state change (plug/unplug)
                if (previous != null && current.IsCharging != previous.IsCharging)
                {
                    _logger.LogDebug("Charging state changed for {Device}. Resetting notification flags.", deviceKey);
                    state.LowFired = false;
                    state.CriticalFired = false;
                    state.FullChargeFired = false;
                    state.LastNotificationTimes.Remove(NotificationType.Low);
                    state.LastNotificationTimes.Remove(NotificationType.Critical);
                    state.LastNotificationTimes.Remove(NotificationType.FullCharge);
                }

                // Full charge notification
                bool isDefinitiveFull = current.IsCharging && current.Level >= 100;
                bool isStableNearFull = current.IsCharging && current.Level >= 99 &&
                                        previous is { IsCharging: true, Level: >= 99 };

                if (!state.FullChargeFired && (isDefinitiveFull || isStableNearFull))
                {
                    if (TryFireNotification(state, NotificationType.FullCharge,
                            "Charging Complete",
                            "Your mouse is fully charged!", cooldown))
                    {
                        state.FullChargeFired = true;
                    }
                }

                // Low battery notification (edge-triggered: was above threshold, now at or below)
                if (!current.IsCharging && !state.LowFired)
                {
                    bool wasAbove = previous == null || previous.Level > settings.LowBatteryThreshold;
                    bool nowAtOrBelow = current.Level <= settings.LowBatteryThreshold;

                    if (wasAbove && nowAtOrBelow && current.Level > settings.CriticalBatteryThreshold)
                    {
                        if (TryFireNotification(state, NotificationType.Low,
                                "Low Battery",
                                $"Battery is low at {current.Level}%.", cooldown))
                        {
                            state.LowFired = true;
                        }
                    }
                }

                // Critical battery notification (edge-triggered)
                if (!current.IsCharging && !state.CriticalFired)
                {
                    bool wasAbove = previous == null || previous.Level > settings.CriticalBatteryThreshold;
                    bool nowAtOrBelow = current.Level <= settings.CriticalBatteryThreshold;

                    if (wasAbove && nowAtOrBelow)
                    {
                        if (TryFireNotification(state, NotificationType.Critical,
                                "Critical Battery",
                                $"Battery is critically low at {current.Level}%!", cooldown))
                        {
                            state.CriticalFired = true;
                        }
                    }
                }

                state.LastLevel = current.Level;
                state.LastIsCharging = current.IsCharging;
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Error processing battery update for notifications");
            }
        }
    }

    private bool TryFireNotification(DeviceNotificationState state, NotificationType type, string title, string message, TimeSpan cooldown)
    {
        DateTime now = DateTime.UtcNow;

        // Check cooldown
        if (state.LastNotificationTimes.TryGetValue(type, out var lastTime))
        {
            if (now - lastTime < cooldown)
            {
                _logger.LogDebug("Notification {Type} suppressed (cooldown active, last fired {Time})",
                    type, lastTime);
                return false;
            }
        }

        state.LastNotificationTimes[type] = now;

        _logger.LogInformation("Firing notification: [{Type}] {Title}: {Message}", type, title, message);

        try
        {
            NotificationTriggered?.Invoke(type, title, message);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error in NotificationTriggered event handler");
        }

        return true;
    }

    private DeviceNotificationState GetOrCreateState(string deviceKey)
    {
        if (!_deviceStates.TryGetValue(deviceKey, out var state))
        {
            state = new DeviceNotificationState();
            _deviceStates[deviceKey] = state;
        }

        return state;
    }

    private class DeviceNotificationState
    {
        public int LastLevel { get; set; }
        public bool LastIsCharging { get; set; }
        public bool LowFired { get; set; }
        public bool CriticalFired { get; set; }
        public bool FullChargeFired { get; set; }
        public Dictionary<NotificationType, DateTime> LastNotificationTimes { get; } = new();
    }
}
