namespace GBM.Core.Models;

public record BatteryEstimate
{
    public bool IsValid { get; init; }
    public TimeSpan TimeRemaining { get; init; }
    public double Confidence { get; init; }
    public string Phase { get; init; } = "discharge";
    public double RatePerHour { get; init; }
    public int SampleCount { get; init; }
    public bool IsHistorical { get; init; }

    public static BatteryEstimate Invalid => new()
    {
        IsValid = false,
        TimeRemaining = TimeSpan.Zero,
        Confidence = 0,
        Phase = "discharge",
        RatePerHour = 0,
        SampleCount = 0
    };
}

public record LearnedRates(
    double? DischargeRate,
    double? ChargeRate,
    int DischargeSessionCount,
    int ChargeSessionCount);

public record ChargeCalibration(
    double OvershootPercent,
    int ObservationCount);
