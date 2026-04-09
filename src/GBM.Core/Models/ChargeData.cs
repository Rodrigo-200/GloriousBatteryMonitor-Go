namespace GBM.Core.Models;

public class ChargeData
{
    public Dictionary<string, DeviceChargeData> Devices { get; set; } = new();
}

public class DeviceChargeData
{
    public string CompositeKey { get; set; } = string.Empty;
    public int LastKnownLevel { get; set; }
    public DateTime LastReadTime { get; set; }
    public DateTime? LastChargeTime { get; set; }
    public int? LastChargeLevel { get; set; }
    public List<BatterySample> Samples { get; set; } = new();

    // Learned historical rates (persisted across sessions for adaptive estimation)
    public double? LearnedDischargeRate { get; set; }
    public double? LearnedChargeRate { get; set; }
    public int DischargeSessionCount { get; set; }
    public int ChargeSessionCount { get; set; }

    // Learned charging overshoot correction, observed after unplug events.
    // Example: estimated 100% while wired, first stable wireless sample is 86% => 14% overshoot.
    public double? LearnedChargeOvershootPercent { get; set; }
    public int ChargeOvershootObservationCount { get; set; }
}

public class BatterySample
{
    public int Level { get; set; }
    public DateTime Timestamp { get; set; }
    public bool IsCharging { get; set; }
}
