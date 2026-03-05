namespace GBM.Core.Models;

public enum ConnectionState
{
    NotConnected,
    Connecting,
    Connected,
    LastKnown,
    Sleeping
}

public enum BatteryHealth
{
    Good,
    Fair,
    Low,
    Critical
}

public record BatteryState
{
    public int Level { get; init; }
    public bool IsCharging { get; init; }
    public ConnectionState Connection { get; init; }
    public BatteryHealth Health { get; init; }
    public string DeviceName { get; init; } = "Glorious Mouse";
    public DateTime LastReadTime { get; init; }
    public DateTime? LastChargeTime { get; init; }
    public int? LastChargeLevel { get; init; }

    public static BatteryState Disconnected => new()
    {
        Level = 0,
        IsCharging = false,
        Connection = ConnectionState.NotConnected,
        Health = BatteryHealth.Good,
        DeviceName = "Glorious Mouse",
        LastReadTime = DateTime.MinValue
    };

    public static BatteryHealth DeriveHealth(int level, int criticalThreshold)
    {
        if (level >= 50) return BatteryHealth.Good;
        if (level >= 20) return BatteryHealth.Fair;
        if (level >= criticalThreshold) return BatteryHealth.Low;
        return BatteryHealth.Critical;
    }
}
