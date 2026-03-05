using System.Text.Json.Serialization;

namespace GBM.Core.Models;

public class AppSettings
{
    public bool StartWithOS { get; set; } = false;
    public bool StartMinimized { get; set; } = false;
    public int RefreshIntervalSeconds { get; set; } = 5;
    public bool NotificationsEnabled { get; set; } = false;
    public int LowBatteryThreshold { get; set; } = 20;
    public int CriticalBatteryThreshold { get; set; } = 10;
    public bool SafeHidMode { get; set; } = true;
    public bool ShowPercentageOnTrayIcon { get; set; } = false;
    public bool NonIntrusiveMode { get; set; } = false;
    public string Theme { get; set; } = "system";

    public AppSettings Clone() => new()
    {
        StartWithOS = StartWithOS,
        StartMinimized = StartMinimized,
        RefreshIntervalSeconds = RefreshIntervalSeconds,
        NotificationsEnabled = NotificationsEnabled,
        LowBatteryThreshold = LowBatteryThreshold,
        CriticalBatteryThreshold = CriticalBatteryThreshold,
        SafeHidMode = SafeHidMode,
        ShowPercentageOnTrayIcon = ShowPercentageOnTrayIcon,
        NonIntrusiveMode = NonIntrusiveMode,
        Theme = Theme
    };
}

[JsonSerializable(typeof(AppSettings))]
[JsonSerializable(typeof(ChargeData))]
[JsonSerializable(typeof(List<DeviceProfile>))]
internal partial class GbmJsonContext : JsonSerializerContext
{
}
