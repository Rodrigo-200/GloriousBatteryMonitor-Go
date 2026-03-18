using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class BatteryEstimationService : IBatteryEstimationService
{
    private readonly ILogger<BatteryEstimationService> _logger;
    private readonly Dictionary<string, DeviceEstimationState> _states = new();
    private readonly object _lock = new();

    private const int MinLevelChangeForEstimate = 3;
    private const int MinRatesForSession = 3;
    private const int MaxSamplesPerDevice = 500;
    private const double MaxEstimateHours = 48.0;
    private const int MaxHistoricalSessions = 20;
    private const double MinReasonableRate = 0.1;
    private const double MaxReasonableRate = 200.0;

    // Default rates for first-ever use (before any learning).
    // These provide an immediate estimate so "Calculating..." never lingers.
    // Discharge: ~1.5%/hr ≈ 67 hours (conservative for wireless gaming mice)
    // Charge: ~50%/hr ≈ 2 hours to full (typical Li-ion CC phase average)
    private const double DefaultDischargeRate = 1.5;
    private const double DefaultChargeRate = 50.0;

    public BatteryEstimationService(ILogger<BatteryEstimationService> logger)
    {
        _logger = logger;
    }

    public BatteryEstimate GetEstimate(string deviceKey)
    {
        lock (_lock)
        {
            if (!_states.TryGetValue(deviceKey, out var state))
                return BatteryEstimate.Invalid;

            return ComputeEstimate(state);
        }
    }

    public void AddSample(string deviceKey, int level, bool isCharging)
    {
        const double ewmaAlpha = 0.25;

        lock (_lock)
        {
            try
            {
                if (!_states.TryGetValue(deviceKey, out var state))
                {
                    state = new DeviceEstimationState();
                    _states[deviceKey] = state;
                }

                // When charging state changes, learn from the ending session before clearing
                if (state.Samples.Count > 0 && state.LastIsCharging != isCharging)
                {
                    LearnFromSession(state, deviceKey);
                    state.Samples.Clear();
                    state.SmoothedRate = null;
                    state.LastEffectiveRate = null;
                }

                state.LastIsCharging = isCharging;

                state.Samples.Add(new EstimationSample
                {
                    Level = level,
                    Timestamp = DateTime.UtcNow,
                    IsCharging = isCharging
                });

                // Compute rate from last two samples and update EWMA
                if (state.Samples.Count >= 2)
                {
                    int prevIndex = state.Samples.Count - 2;
                    int currIndex = state.Samples.Count - 1;
                    var prev = state.Samples[prevIndex];
                    var curr = state.Samples[currIndex];

                    double elapsedHours = (curr.Timestamp - prev.Timestamp).TotalHours;
                    if (elapsedHours > 0)
                    {
                        double instantRate = (curr.Level - prev.Level) / elapsedHours;

                        // Prime on first rate or apply EWMA
                        if (!state.SmoothedRate.HasValue)
                        {
                            state.SmoothedRate = instantRate;
                        }
                        else
                        {
                            state.SmoothedRate = ewmaAlpha * instantRate + (1.0 - ewmaAlpha) * state.SmoothedRate.Value;
                        }
                    }
                }

                // Trim samples
                if (state.Samples.Count > MaxSamplesPerDevice)
                {
                    state.Samples.RemoveRange(0, state.Samples.Count - MaxSamplesPerDevice);
                }
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Error adding estimation sample for device {Key}", deviceKey);
            }
        }
    }

    public void Reset(string deviceKey)
    {
        lock (_lock)
        {
            if (_states.TryGetValue(deviceKey, out var state))
            {
                // Learn from the current session before resetting
                LearnFromSession(state, deviceKey);
                state.Samples.Clear();
                _logger.LogDebug("Estimation state reset for device {Key}", deviceKey);
            }
        }
    }

    public void SetHistoricalRates(string deviceKey, double? dischargeRate, double? chargeRate,
                                    int dischargeSessionCount, int chargeSessionCount)
    {
        lock (_lock)
        {
            if (!_states.TryGetValue(deviceKey, out var state))
            {
                state = new DeviceEstimationState();
                _states[deviceKey] = state;
            }

            state.HistoricalDischargeRate = dischargeRate;
            state.HistoricalChargeRate = chargeRate;
            state.DischargeSessionCount = dischargeSessionCount;
            state.ChargeSessionCount = chargeSessionCount;

            _logger.LogInformation(
                "Historical rates loaded for {Key}: discharge={DischargeRate}%/hr ({DischargeSessions} sessions), " +
                "charge={ChargeRate}%/hr ({ChargeSessions} sessions)",
                deviceKey,
                dischargeRate?.ToString("F1") ?? "none", dischargeSessionCount,
                chargeRate?.ToString("F1") ?? "none", chargeSessionCount);
        }
    }

    public LearnedRates? GetLearnedRates(string deviceKey)
    {
        lock (_lock)
        {
            if (!_states.TryGetValue(deviceKey, out var state))
                return null;

            if (!state.HistoricalDischargeRate.HasValue && !state.HistoricalChargeRate.HasValue)
                return null;

            return new LearnedRates(
                state.HistoricalDischargeRate,
                state.HistoricalChargeRate,
                state.DischargeSessionCount,
                state.ChargeSessionCount);
        }
    }

    private void LearnFromSession(DeviceEstimationState state, string deviceKey)
    {
        // Use the EWMA-smoothed rate directly
        double? sessionRate = state.SmoothedRate;

        if (!sessionRate.HasValue)
            return;

        double absRate = Math.Abs(sessionRate.Value);
        if (absRate < MinReasonableRate || absRate > MaxReasonableRate)
        {
            _logger.LogDebug("Session rate {Rate}%/hr outside reasonable range, skipping learning", absRate);
            return;
        }

        if (state.LastIsCharging)
        {
            double? rate = state.HistoricalChargeRate;
            int count = state.ChargeSessionCount;
            BlendRate(ref rate, ref count, absRate);
            state.HistoricalChargeRate = rate;
            state.ChargeSessionCount = count;
            _logger.LogInformation(
                "Learned charge rate for {Key}: session={SessionRate:F1}%/hr, " +
                "historical={HistRate:F1}%/hr ({Sessions} sessions)",
                deviceKey, absRate, state.HistoricalChargeRate, state.ChargeSessionCount);
        }
        else
        {
            double? rate = state.HistoricalDischargeRate;
            int count = state.DischargeSessionCount;
            BlendRate(ref rate, ref count, absRate);
            state.HistoricalDischargeRate = rate;
            state.DischargeSessionCount = count;
            _logger.LogInformation(
                "Learned discharge rate for {Key}: session={SessionRate:F1}%/hr, " +
                "historical={HistRate:F1}%/hr ({Sessions} sessions)",
                deviceKey, absRate, state.HistoricalDischargeRate, state.DischargeSessionCount);
        }
    }

    /// <summary>
    /// Blend a new session rate into the historical average using a capped cumulative average.
    /// The cap ensures old data doesn't dominate forever — recent sessions always have influence.
    /// </summary>
    private static void BlendRate(ref double? historicalRate, ref int sessionCount, double sessionRate)
    {
        if (!historicalRate.HasValue || sessionCount <= 0)
        {
            historicalRate = sessionRate;
            sessionCount = 1;
        }
        else
        {
            int cappedCount = Math.Min(sessionCount, MaxHistoricalSessions);
            historicalRate = (historicalRate.Value * cappedCount + sessionRate) / (cappedCount + 1);
            sessionCount++;
        }
    }

    private BatteryEstimate ComputeEstimate(DeviceEstimationState state)
    {
        try
        {
            var samples = state.Samples;
            bool isCharging = state.LastIsCharging;
            int lastLevel = samples.Count > 0 ? samples[^1].Level : 0;

            // Try to compute session rate
            double? sessionAvgRate = null;
            bool hasSessionData = false;

            if (samples.Count >= 2)
            {
                int firstLevel = samples[0].Level;
                int totalChange = Math.Abs(lastLevel - firstLevel);

                if (totalChange >= MinLevelChangeForEstimate && state.SmoothedRate.HasValue)
                {
                    sessionAvgRate = state.SmoothedRate.Value;
                    hasSessionData = Math.Abs(sessionAvgRate.Value) >= 0.01;
                }
            }

            // Get historical rate for this phase
            double? historicalRate = isCharging ? state.HistoricalChargeRate : state.HistoricalDischargeRate;
            int historicalSessions = isCharging ? state.ChargeSessionCount : state.DischargeSessionCount;
            bool hasHistorical = historicalRate.HasValue && historicalRate.Value > 0 && historicalSessions > 0;

            // Determine effective rate with direction validation (warn but don't reject)
            double effectiveRate;
            bool isHistorical;

            if (hasSessionData && hasHistorical)
            {
                // Blend session and historical rates
                double absSessionRate = Math.Abs(sessionAvgRate!.Value);

                // Check for direction mismatch and log warning (but don't return Invalid)
                if (isCharging && sessionAvgRate!.Value <= 0)
                {
                    _logger.LogWarning("Session rate {Rate}%/hr has wrong sign for charging phase, falling through to historical", sessionAvgRate!.Value);
                    hasSessionData = false;
                }
                else if (!isCharging && sessionAvgRate!.Value >= 0)
                {
                    _logger.LogWarning("Session rate {Rate}%/hr has wrong sign for discharge phase, falling through to historical", sessionAvgRate!.Value);
                    hasSessionData = false;
                }

                if (hasSessionData)
                {
                    double histWeight = Math.Min(historicalSessions, MaxHistoricalSessions);
                    double sessionWeight = 1.0;  // SmoothedRate counts as 1 effective sample
                    double blendedRate = (historicalRate!.Value * histWeight + absSessionRate * sessionWeight)
                                         / (histWeight + sessionWeight);

                    effectiveRate = isCharging ? blendedRate : -blendedRate;
                    isHistorical = false;
                }
                else
                {
                    // Fallback to historical
                    effectiveRate = isCharging ? historicalRate!.Value : -historicalRate!.Value;
                    isHistorical = true;
                }
            }
            else if (hasSessionData)
            {
                // Session only
                if (isCharging && sessionAvgRate!.Value <= 0)
                {
                    _logger.LogWarning("Session rate {Rate}%/hr has wrong sign for charging phase, falling through to historical/default", sessionAvgRate!.Value);
                    hasSessionData = false;
                }
                else if (!isCharging && sessionAvgRate!.Value >= 0)
                {
                    _logger.LogWarning("Session rate {Rate}%/hr has wrong sign for discharge phase, falling through to historical/default", sessionAvgRate!.Value);
                    hasSessionData = false;
                }

                if (hasSessionData)
                {
                    effectiveRate = sessionAvgRate!.Value;
                    isHistorical = false;
                }
                else if (hasHistorical && lastLevel > 0)
                {
                    // Fallback to historical
                    effectiveRate = isCharging ? historicalRate!.Value : -historicalRate!.Value;
                    isHistorical = true;
                }
                else if (lastLevel > 0)
                {
                    // No historical, no session data — use built-in defaults
                    double defaultRate = isCharging ? DefaultChargeRate : DefaultDischargeRate;
                    effectiveRate = isCharging ? defaultRate : -defaultRate;
                    isHistorical = true;
                }
                else
                {
                    // No battery level at all (device not connected yet)
                    return BatteryEstimate.Invalid;
                }
            }
            else if (hasHistorical && lastLevel > 0)
            {
                // Historical only (startup / early session)
                effectiveRate = isCharging ? historicalRate!.Value : -historicalRate!.Value;
                isHistorical = true;
            }
            else if (lastLevel > 0)
            {
                // No historical, no session data — use built-in defaults.
                // This ensures a time estimate from the very first poll (like phones do).
                double defaultRate = isCharging ? DefaultChargeRate : DefaultDischargeRate;
                effectiveRate = isCharging ? defaultRate : -defaultRate;
                isHistorical = true;
            }
            else
            {
                // No battery level at all (device not connected yet)
                return BatteryEstimate.Invalid;
            }

            // Apply stability gate: if new rate is within 10% of last rate, use the last one
            const double stabilityThreshold = 0.1;
            if (state.LastEffectiveRate.HasValue)
            {
                double lastRate = state.LastEffectiveRate.Value;
                double percentDiff = Math.Abs(effectiveRate - lastRate) / Math.Abs(lastRate);
                if (percentDiff < stabilityThreshold)
                {
                    effectiveRate = lastRate;
                }
            }

            // Store as last effective rate for next time
            state.LastEffectiveRate = effectiveRate;

            // Compute time remaining
            string phase;
            double hoursRemaining;

            if (isCharging)
            {
                hoursRemaining = (100.0 - lastLevel) / Math.Abs(effectiveRate);
                phase = "charge";
            }
            else
            {
                hoursRemaining = lastLevel / Math.Abs(effectiveRate);
                phase = "discharge";
            }

            hoursRemaining = Math.Min(hoursRemaining, MaxEstimateHours);
            hoursRemaining = Math.Max(hoursRemaining, 0);

            // Compute confidence using SmoothedRate in a synthetic list
            double confidence;
            if (isHistorical)
            {
                confidence = ComputeHistoricalConfidence(historicalSessions);
            }
            else if (hasSessionData && state.SmoothedRate.HasValue)
            {
                // Use SmoothedRate in a synthetic list for consistency calculation
                var syntheticRates = new List<double> { state.SmoothedRate.Value };
                confidence = ComputeConfidence(syntheticRates, samples.Count);
                if (hasHistorical)
                {
                    // Boost confidence when we have both session and historical backing
                    confidence = Math.Min(1.0, confidence + 0.1);
                }
            }
            else
            {
                confidence = 0.1;
            }

            return new BatteryEstimate
            {
                IsValid = true,
                TimeRemaining = TimeSpan.FromHours(hoursRemaining),
                Confidence = confidence,
                Phase = phase,
                RatePerHour = Math.Abs(effectiveRate),
                SampleCount = samples.Count,
                IsHistorical = isHistorical
            };
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error computing battery estimate");
            return BatteryEstimate.Invalid;
        }
    }

    private static double ComputeConfidence(List<double> rates, int sampleCount)
    {
        if (rates.Count < 2)
            return 0.1;

        // Factor 1: Sample count contribution (0 to 0.5)
        double sampleFactor = Math.Min(sampleCount / 20.0, 1.0) * 0.5;

        // Factor 2: Consistency / low standard deviation (0 to 0.5)
        double mean = rates.Average();
        double variance = rates.Sum(r => (r - mean) * (r - mean)) / rates.Count;
        double stdDev = Math.Sqrt(variance);

        // Lower std dev relative to mean = higher consistency
        double relativeStdDev = Math.Abs(mean) > 0.01 ? stdDev / Math.Abs(mean) : stdDev;
        double consistencyFactor = Math.Max(0, 1.0 - relativeStdDev) * 0.5;

        return Math.Clamp(sampleFactor + consistencyFactor, 0.0, 1.0);
    }

    /// <summary>
    /// Confidence for estimates based solely on historical data.
    /// Increases with more sessions but never reaches 1.0 without current data.
    /// </summary>
    private static double ComputeHistoricalConfidence(int sessionCount)
    {
        // 1 session → 0.3, 5 → 0.5, 10 → 0.6, 20+ → 0.7
        double factor = Math.Min(sessionCount / 20.0, 1.0);
        return 0.3 + factor * 0.4;
    }

    private class DeviceEstimationState
    {
        public List<EstimationSample> Samples { get; } = new();
        public bool LastIsCharging { get; set; }
        public double? HistoricalDischargeRate { get; set; }
        public double? HistoricalChargeRate { get; set; }
        public int DischargeSessionCount { get; set; }
        public int ChargeSessionCount { get; set; }

        // EWMA smoothed instantaneous discharge/charge rate (per sample pair)
        public double? SmoothedRate { get; set; }

        // Last effective rate used for time-remaining calculation (stability gate)
        public double? LastEffectiveRate { get; set; }
    }

    private class EstimationSample
    {
        public int Level { get; init; }
        public DateTime Timestamp { get; init; }
        public bool IsCharging { get; init; }
    }
}
