using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class BatteryEstimationService : IBatteryEstimationService
{
    private readonly ILogger<BatteryEstimationService> _logger;
    private readonly Dictionary<string, DeviceEstimationState> _states = new();
    private readonly object _lock = new();

    private const int MinLevelChangeForEstimate = 3;
    private const int MaxSamplesPerDevice = 500;
    // Keep a very high safety cap to avoid infinite/absurd durations while still
    // allowing multi-day estimates for low-drain real-world usage patterns.
    private const double MaxEstimateHours = 24.0 * 30.0;
    private const int MaxHistoricalSessions = 20;
    private const double MinReasonableRate = 0.1;
    private const double MaxReasonableRate = 200.0;
    private const double MaxReasonableDischargeLearnRate = 15.0;
    private const double MinReasonableChargeLearnRate = 5.0;
    private const double MaxReasonableChargeLearnRate = 120.0;
    private const int MinSessionsForOutlierGuard = 3;
    private const double MinOutlierRatioVsHistorical = 0.33;
    private const double MaxOutlierRatioVsHistorical = 3.0;
    private static readonly TimeSpan MinSpanForOngoingDischargeLearning = TimeSpan.FromMinutes(45);
    private static readonly TimeSpan MinSpanForOngoingChargeLearning = TimeSpan.FromMinutes(15);
    private const int MinLevelDeltaForOngoingDischargeLearning = 1;
    private const int MinLevelDeltaForOngoingChargeLearning = 2;
    private static readonly TimeSpan MinSpanForLowDeltaEstimate = TimeSpan.FromHours(2);

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
        AddSampleInternal(deviceKey, level, isCharging, DateTime.UtcNow);
    }

    internal void AddSampleForTesting(string deviceKey, int level, bool isCharging, DateTime timestampUtc)
    {
        AddSampleInternal(deviceKey, level, isCharging, timestampUtc);
    }

    private void AddSampleInternal(string deviceKey, int level, bool isCharging, DateTime timestampUtc)
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
                    state.LastLearnCheckpointLevel = null;
                    state.LastLearnCheckpointTimeUtc = null;
                }

                state.LastIsCharging = isCharging;

                state.Samples.Add(new EstimationSample
                {
                    Level = level,
                    Timestamp = timestampUtc,
                    IsCharging = isCharging
                });

                if (!state.LastLearnCheckpointTimeUtc.HasValue)
                {
                    state.LastLearnCheckpointTimeUtc = timestampUtc;
                    state.LastLearnCheckpointLevel = level;
                }

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

                TryLearnFromOngoingSession(state, deviceKey);
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
                state.SmoothedRate = null;
                state.LastEffectiveRate = null;
                state.LastLearnCheckpointLevel = null;
                state.LastLearnCheckpointTimeUtc = null;
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
        if (state.Samples.Count < 2 || !state.SmoothedRate.HasValue)
            return;

        var firstSample = state.Samples[0];
        var lastSample = state.Samples[^1];
        if (!HasEnoughLearningEvidence(
                state,
                firstSample.Timestamp,
                firstSample.Level,
                lastSample.Timestamp,
                lastSample.Level))
        {
            return;
        }

        LearnRate(state, deviceKey, Math.Abs(state.SmoothedRate.Value), source: "phase-end");
    }

    private void TryLearnFromOngoingSession(DeviceEstimationState state, string deviceKey)
    {
        if (state.Samples.Count < 2 || !state.SmoothedRate.HasValue)
            return;

        var lastSample = state.Samples[^1];
        DateTime checkpointTime = state.LastLearnCheckpointTimeUtc ?? state.Samples[0].Timestamp;
        int checkpointLevel = state.LastLearnCheckpointLevel ?? state.Samples[0].Level;

        if (!HasEnoughLearningEvidence(
                state,
                checkpointTime,
                checkpointLevel,
                lastSample.Timestamp,
                lastSample.Level))
        {
            return;
        }

        LearnRate(state, deviceKey, Math.Abs(state.SmoothedRate.Value), source: "checkpoint");
        state.LastLearnCheckpointTimeUtc = lastSample.Timestamp;
        state.LastLearnCheckpointLevel = lastSample.Level;
    }

    private static bool HasEnoughLearningEvidence(
        DeviceEstimationState state,
        DateTime fromTime,
        int fromLevel,
        DateTime toTime,
        int toLevel)
    {
        if (toTime <= fromTime)
            return false;

        TimeSpan span = toTime - fromTime;
        int levelDelta = Math.Abs(toLevel - fromLevel);

        TimeSpan minSpan = state.LastIsCharging
            ? MinSpanForOngoingChargeLearning
            : MinSpanForOngoingDischargeLearning;

        int minLevelDelta = state.LastIsCharging
            ? MinLevelDeltaForOngoingChargeLearning
            : MinLevelDeltaForOngoingDischargeLearning;

        return span >= minSpan && levelDelta >= minLevelDelta;
    }

    private void LearnRate(DeviceEstimationState state, string deviceKey, double absRate, string source)
    {
        if (!IsReasonableLearnRate(state, absRate, out string reason))
        {
            _logger.LogDebug(
                "Skipping {Source} learning for {Key}: {Reason}",
                source,
                deviceKey,
                reason);
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
                "Learned charge rate for {Key}: source={Source}, session={SessionRate:F1}%/hr, " +
                "historical={HistRate:F1}%/hr ({Sessions} sessions)",
                deviceKey, source, absRate, state.HistoricalChargeRate, state.ChargeSessionCount);
        }
        else
        {
            double? rate = state.HistoricalDischargeRate;
            int count = state.DischargeSessionCount;
            BlendRate(ref rate, ref count, absRate);
            state.HistoricalDischargeRate = rate;
            state.DischargeSessionCount = count;
            _logger.LogInformation(
                "Learned discharge rate for {Key}: source={Source}, session={SessionRate:F1}%/hr, " +
                "historical={HistRate:F1}%/hr ({Sessions} sessions)",
                deviceKey, source, absRate, state.HistoricalDischargeRate, state.DischargeSessionCount);
        }
    }

    private bool IsReasonableLearnRate(DeviceEstimationState state, double absRate, out string reason)
    {
        if (absRate < MinReasonableRate || absRate > MaxReasonableRate)
        {
            reason = $"rate {absRate:F2}%/hr outside global bounds";
            return false;
        }

        if (state.LastIsCharging)
        {
            if (absRate < MinReasonableChargeLearnRate || absRate > MaxReasonableChargeLearnRate)
            {
                reason = $"charge rate {absRate:F2}%/hr outside charge learning bounds";
                return false;
            }

            if (state.HistoricalChargeRate.HasValue &&
                state.ChargeSessionCount >= MinSessionsForOutlierGuard &&
                IsRateOutlierVsHistorical(absRate, state.HistoricalChargeRate.Value))
            {
                reason = $"charge rate {absRate:F2}%/hr is outlier vs historical {state.HistoricalChargeRate.Value:F2}%/hr";
                return false;
            }
        }
        else
        {
            if (absRate > MaxReasonableDischargeLearnRate)
            {
                reason = $"discharge rate {absRate:F2}%/hr outside discharge learning bounds";
                return false;
            }

            if (state.HistoricalDischargeRate.HasValue &&
                state.DischargeSessionCount >= MinSessionsForOutlierGuard &&
                IsRateOutlierVsHistorical(absRate, state.HistoricalDischargeRate.Value))
            {
                reason = $"discharge rate {absRate:F2}%/hr is outlier vs historical {state.HistoricalDischargeRate.Value:F2}%/hr";
                return false;
            }
        }

        reason = string.Empty;
        return true;
    }

    private static bool IsRateOutlierVsHistorical(double absRate, double historicalRate)
    {
        if (historicalRate <= 0)
            return false;

        double ratio = absRate / historicalRate;
        return ratio < MinOutlierRatioVsHistorical || ratio > MaxOutlierRatioVsHistorical;
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
            if (samples.Count == 0)
                return BatteryEstimate.Invalid;

            int lastLevel = samples[^1].Level;
            if (lastLevel <= 0)
                return BatteryEstimate.Invalid;

            var firstSample = samples[0];
            var lastSample = samples[^1];
            TimeSpan sampleSpan = samples.Count > 1 ? lastSample.Timestamp - firstSample.Timestamp : TimeSpan.Zero;
            TimeSpan recency = DateTime.UtcNow - lastSample.Timestamp;

            double defaultAbsRate = isCharging ? DefaultChargeRate : DefaultDischargeRate;

            // Try to compute session rate
            double? sessionAvgRate = null;
            bool hasSessionData = false;

            if (samples.Count >= 2 && state.SmoothedRate.HasValue)
            {
                int firstLevel = samples[0].Level;
                int totalChange = Math.Abs(lastLevel - firstLevel);
                bool enoughSessionEvidence = totalChange >= MinLevelChangeForEstimate ||
                                             (totalChange >= 1 && sampleSpan >= MinSpanForLowDeltaEstimate);

                if (enoughSessionEvidence)
                {
                    sessionAvgRate = state.SmoothedRate.Value;
                    hasSessionData = Math.Abs(sessionAvgRate.Value) >= 0.01;

                    if (hasSessionData && isCharging && sessionAvgRate.Value <= 0)
                    {
                        _logger.LogWarning(
                            "Session rate {Rate}%/hr has wrong sign for charging phase, falling through to learned/default",
                            sessionAvgRate.Value);
                        hasSessionData = false;
                    }
                    else if (hasSessionData && !isCharging && sessionAvgRate.Value >= 0)
                    {
                        _logger.LogWarning(
                            "Session rate {Rate}%/hr has wrong sign for discharge phase, falling through to learned/default",
                            sessionAvgRate.Value);
                        hasSessionData = false;
                    }
                }
            }

            // Get historical rate for this phase
            double? historicalRate = isCharging ? state.HistoricalChargeRate : state.HistoricalDischargeRate;
            int historicalSessions = isCharging ? state.ChargeSessionCount : state.DischargeSessionCount;
            bool hasHistorical = historicalRate.HasValue && historicalRate.Value > 0 && historicalSessions > 0;
            double historicalConfidence = hasHistorical ? ComputeHistoricalConfidence(historicalSessions) : 0.0;
            double blendedHistoricalAbsRate = hasHistorical
                ? BlendLearnedRateWithDefault(historicalRate!.Value, defaultAbsRate, historicalConfidence)
                : defaultAbsRate;

            var sessionRates = GetSessionRateSeries(samples, isCharging);
            double sessionConfidence = ComputeSessionConfidence(sessionRates, samples.Count, sampleSpan, recency);

            // Determine effective rate with direction validation (warn but don't reject)
            double effectiveAbsRate;
            bool isHistorical;

            if (hasSessionData && hasHistorical)
            {
                // Blend current session with learned history. Better evidence carries more weight.
                double sessionWeight = Math.Max(0.25, sessionConfidence);
                double historicalWeight = Math.Max(0.15, historicalConfidence);
                effectiveAbsRate = (Math.Abs(sessionAvgRate!.Value) * sessionWeight + blendedHistoricalAbsRate * historicalWeight)
                                   / (sessionWeight + historicalWeight);
                isHistorical = false;
            }
            else if (hasSessionData)
            {
                // Session only: blend against defaults until confidence grows.
                double sessionWeight = Math.Clamp(sessionConfidence, 0.0, 1.0);
                effectiveAbsRate = defaultAbsRate * (1.0 - sessionWeight) + Math.Abs(sessionAvgRate!.Value) * sessionWeight;
                isHistorical = false;
            }
            else if (hasHistorical)
            {
                // Historical only (startup / early session): blend learned rates with defaults
                // unless confidence from session count is already strong.
                effectiveAbsRate = blendedHistoricalAbsRate;
                isHistorical = true;
            }
            else
            {
                // No historical, no session data — use built-in defaults.
                // This ensures a time estimate from the very first poll (like phones do).
                effectiveAbsRate = defaultAbsRate;
                isHistorical = true;
            }

            if (effectiveAbsRate < MinReasonableRate || effectiveAbsRate > MaxReasonableRate)
            {
                effectiveAbsRate = defaultAbsRate;
            }

            double effectiveRate = isCharging ? effectiveAbsRate : -effectiveAbsRate;

            // Apply stability gate: if new rate is within 10% of last rate, use the last one
            const double stabilityThreshold = 0.1;
            if (state.LastEffectiveRate.HasValue && Math.Abs(state.LastEffectiveRate.Value) > 0.001)
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
                hoursRemaining = (100.0 - lastLevel) / effectiveAbsRate;
                phase = "charge";
            }
            else
            {
                hoursRemaining = lastLevel / effectiveAbsRate;
                phase = "discharge";
            }

            hoursRemaining = Math.Min(hoursRemaining, MaxEstimateHours);
            hoursRemaining = Math.Max(hoursRemaining, 0);

            // Compute confidence using SmoothedRate in a synthetic list
            double confidence;
            if (hasSessionData && hasHistorical)
            {
                confidence = Math.Clamp(sessionConfidence * 0.7 + historicalConfidence * 0.3, 0.15, 1.0);
            }
            else if (hasSessionData)
            {
                confidence = Math.Clamp(sessionConfidence, 0.12, 1.0);
            }
            else if (isHistorical && hasHistorical)
            {
                confidence = historicalConfidence;
            }
            else
            {
                confidence = 0.15;
            }

            return new BatteryEstimate
            {
                IsValid = true,
                TimeRemaining = TimeSpan.FromHours(hoursRemaining),
                Confidence = confidence,
                Phase = phase,
                RatePerHour = effectiveAbsRate,
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

    private static double BlendLearnedRateWithDefault(double learnedRate, double defaultRate, double confidence)
    {
        double learnedWeight = Math.Clamp((confidence - 0.2) / 0.6, 0.0, 1.0);
        return defaultRate * (1.0 - learnedWeight) + learnedRate * learnedWeight;
    }

    private static List<double> GetSessionRateSeries(List<EstimationSample> samples, bool isCharging)
    {
        var rates = new List<double>(Math.Max(0, samples.Count - 1));

        for (int i = 1; i < samples.Count; i++)
        {
            var prev = samples[i - 1];
            var curr = samples[i];
            double elapsedHours = (curr.Timestamp - prev.Timestamp).TotalHours;
            if (elapsedHours <= 0)
                continue;

            double signedRate = (curr.Level - prev.Level) / elapsedHours;
            if (isCharging && signedRate <= 0)
                continue;
            if (!isCharging && signedRate >= 0)
                continue;

            double absRate = Math.Abs(signedRate);
            if (absRate < MinReasonableRate || absRate > MaxReasonableRate)
                continue;

            rates.Add(absRate);
        }

        return rates;
    }

    private static double ComputeSessionConfidence(
        List<double> rates,
        int sampleCount,
        TimeSpan sampleSpan,
        TimeSpan recency)
    {
        if (sampleCount < 2 || rates.Count == 0)
            return 0.12;

        // Factor 1: sample count (more points => better confidence)
        double sampleFactor = Math.Clamp((sampleCount - 1) / 20.0, 0.0, 1.0);

        // Factor 2: time span (a trend over hours is stronger than short bursts)
        double spanFactor = Math.Clamp(sampleSpan.TotalHours / 6.0, 0.0, 1.0);

        // Factor 3: variance (lower spread => more stable behavior)
        double mean = rates.Average();
        double variance = rates.Sum(r => (r - mean) * (r - mean)) / rates.Count;
        double stdDev = Math.Sqrt(variance);
        double relativeStdDev = mean > 0.01 ? stdDev / mean : 1.0;
        double varianceFactor = Math.Clamp(1.0 - relativeStdDev, 0.0, 1.0);

        // Factor 4: recency (stale data should be less trusted)
        double recencyFactor = Math.Clamp(1.0 - (recency.TotalMinutes / 30.0), 0.0, 1.0);

        double confidence = 0.10 +
                            sampleFactor * 0.30 +
                            spanFactor * 0.25 +
                            varianceFactor * 0.20 +
                            recencyFactor * 0.15;
        return Math.Clamp(confidence, 0.12, 1.0);
    }

    /// <summary>
    /// Confidence for estimates based solely on historical data.
    /// Increases with more sessions but never reaches 1.0 without current data.
    /// </summary>
    private static double ComputeHistoricalConfidence(int sessionCount)
    {
        if (sessionCount <= 0)
            return 0.15;

        // Confidence rises smoothly with session count and plateaus below 1.0.
        double sessionFactor = 1.0 - Math.Exp(-sessionCount / 6.0);
        return Math.Clamp(0.25 + sessionFactor * 0.55, 0.25, 0.8);
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

        // Last checkpoint used for ongoing learning inside a single phase.
        public DateTime? LastLearnCheckpointTimeUtc { get; set; }
        public int? LastLearnCheckpointLevel { get; set; }
    }

    private class EstimationSample
    {
        public int Level { get; init; }
        public DateTime Timestamp { get; init; }
        public bool IsCharging { get; init; }
    }
}
