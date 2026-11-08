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
    public string CriticalBatterySoundPath { get; set; } = string.Empty;
    public bool DebugLogging { get; set; } = false;
    public bool ShowPercentageOnTrayIcon { get; set; } = false;
    public string Theme { get; set; } = "system";
    public string Language { get; set; } = "system";
    public bool EnableBetaUpdates { get; set; } = false;
    public bool HasSetBetaChannelPreference { get; set; } = false;
    public int? MainWindowX { get; set; }
    public int? MainWindowY { get; set; }
    public int? BatteryWidgetX { get; set; }
    public int? BatteryWidgetY { get; set; }
    public int BatteryWidgetPositionVersion { get; set; }

    public AppSettings Clone() => new()
    {
        StartWithOS = StartWithOS,
        StartMinimized = StartMinimized,
        RefreshIntervalSeconds = RefreshIntervalSeconds,
        NotificationsEnabled = NotificationsEnabled,
        LowBatteryThreshold = LowBatteryThreshold,
        CriticalBatteryThreshold = CriticalBatteryThreshold,
        NotificationCooldownMinutes = NotificationCooldownMinutes,
        CriticalBatterySoundPath = CriticalBatterySoundPath,
        DebugLogging = DebugLogging,
        ShowPercentageOnTrayIcon = ShowPercentageOnTrayIcon,
        Theme = Theme,
        Language = Language,
        EnableBetaUpdates = EnableBetaUpdates,
        HasSetBetaChannelPreference = HasSetBetaChannelPreference,
        MainWindowX = MainWindowX,
        MainWindowY = MainWindowY,
        BatteryWidgetX = BatteryWidgetX,
        BatteryWidgetY = BatteryWidgetY,
        BatteryWidgetPositionVersion = BatteryWidgetPositionVersion
    };
}

[JsonSerializable(typeof(AppSettings))]
[JsonSerializable(typeof(ChargeData))]
[JsonSerializable(typeof(List<DeviceProfile>))]
public partial class GbmJsonContext : JsonSerializerContext
{
}
