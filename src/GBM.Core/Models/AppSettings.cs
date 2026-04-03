using System.Text.Json.Serialization;

namespace GBM.Core.Models;

public class AppSettings
{
    public bool StartWithOS { get; set; } = false;
    public bool StartMinimized { get; set; } = false;
    public int RefreshIntervalSeconds { get; set; } = 5;
    public bool NotificationsEnabled { get; set; } = true;
    public int LowBatteryThreshold { get; set; } = 20;
    public int CriticalBatteryThreshold { get; set; } = 10;
    public int NotificationCooldownMinutes { get; set; } = 5;
    public bool DebugLogging { get; set; } = false;
    public bool ShowPercentageOnTrayIcon { get; set; } = false;
    public string Theme { get; set; } = "system";
    public bool EnableBetaUpdates { get; set; } = false;

    public AppSettings Clone() => new()
    {
        StartWithOS = StartWithOS,
        StartMinimized = StartMinimized,
        RefreshIntervalSeconds = RefreshIntervalSeconds,
        NotificationsEnabled = NotificationsEnabled,
        LowBatteryThreshold = LowBatteryThreshold,
        CriticalBatteryThreshold = CriticalBatteryThreshold,
        NotificationCooldownMinutes = NotificationCooldownMinutes,
        DebugLogging = DebugLogging,
        ShowPercentageOnTrayIcon = ShowPercentageOnTrayIcon,
        Theme = Theme,
        EnableBetaUpdates = EnableBetaUpdates
    };
}

[JsonSerializable(typeof(AppSettings))]
[JsonSerializable(typeof(ChargeData))]
[JsonSerializable(typeof(List<DeviceProfile>))]
public partial class GbmJsonContext : JsonSerializerContext
{
}
