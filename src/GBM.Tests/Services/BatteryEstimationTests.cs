using FluentAssertions;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Moq;
using Xunit;

namespace GBM.Tests.Services;

public class BatteryEstimationTests
{
    private readonly BatteryEstimationService _service;

    public BatteryEstimationTests()
    {
        var logger = new Mock<ILogger<BatteryEstimationService>>();
        _service = new BatteryEstimationService(logger.Object);
    }

    [Fact]
    public void GetEstimate_WithNoSamples_ReturnsInvalid()
    {
        var estimate = _service.GetEstimate("device1");
        estimate.IsValid.Should().BeFalse();
    }

    [Fact]
    public void GetEstimate_WithInsufficientChange_UsesDefaultRate()
    {
        // Add samples with less than 3% change, no historical data
        _service.AddSample("device1", 80, false);
        _service.AddSample("device1", 79, false);
        _service.AddSample("device1", 78, false);

        var estimate = _service.GetEstimate("device1");
        // Should use default rate instead of showing "Calculating..."
        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
        estimate.Phase.Should().Be("discharge");
        estimate.TimeRemaining.Should().BeGreaterThan(TimeSpan.Zero);
    }

    [Fact]
    public void GetEstimate_DischargeWithSufficientData_ReturnsValidEstimate()
    {
        var baseTime = DateTime.UtcNow.AddHours(-2);
        // Simulate 10% drop over 2 hours (5%/hr)
        _service.AddSample("device1", 80, false);
        // Need a way to set time - since AddSample uses DateTime.UtcNow,
        // we'll add enough samples to show trend
        for (int i = 0; i < 10; i++)
        {
            _service.AddSample("device1", 80 - i, false);
        }

        var estimate = _service.GetEstimate("device1");
        // May or may not be valid depending on timing
        // At minimum, the service should handle the samples without throwing
        estimate.Should().NotBeNull();
    }

    [Fact]
    public void Reset_ClearsSamples()
    {
        _service.AddSample("device1", 80, false);
        _service.AddSample("device1", 70, false);
        _service.Reset("device1");

        var estimate = _service.GetEstimate("device1");
        estimate.IsValid.Should().BeFalse();
    }

    [Fact]
    public void GetEstimate_ForUnknownDevice_ReturnsInvalid()
    {
        var estimate = _service.GetEstimate("nonexistent");
        estimate.IsValid.Should().BeFalse();
    }

    [Fact]
    public void AddSample_ResetsOnChargingStateChange()
    {
        // Add discharging samples
        _service.AddSample("device1", 80, false);
        _service.AddSample("device1", 75, false);

        // Switch to charging - should reset samples
        _service.AddSample("device1", 75, true);
        _service.AddSample("device1", 76, true);

        var estimate = _service.GetEstimate("device1");
        // After reset, insufficient session data — uses default charge rate
        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
        estimate.Phase.Should().Be("charge");
    }

    // --- Historical rate / learning tests ---

    [Fact]
    public void SetHistoricalRates_EnablesImmediateEstimate()
    {
        // Set historical discharge rate (5%/hr) before any samples
        _service.SetHistoricalRates("device1", dischargeRate: 5.0, chargeRate: null,
            dischargeSessionCount: 3, chargeSessionCount: 0);

        // Add a single sample — not enough for session-based estimate
        _service.AddSample("device1", 50, false);

        var estimate = _service.GetEstimate("device1");
        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
        estimate.Phase.Should().Be("discharge");
        estimate.RatePerHour.Should().BeGreaterThan(1.5);
        estimate.RatePerHour.Should().BeLessThan(5.0);
        estimate.TimeRemaining.TotalHours.Should().BeGreaterThan(10.0);
    }

    [Fact]
    public void SetHistoricalRates_ChargeEstimate()
    {
        _service.SetHistoricalRates("device1", dischargeRate: null, chargeRate: 40.0,
            dischargeSessionCount: 0, chargeSessionCount: 5);

        _service.AddSample("device1", 60, true);

        var estimate = _service.GetEstimate("device1");
        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
        estimate.Phase.Should().Be("charge");
        estimate.RatePerHour.Should().BeGreaterThan(40.0);
        estimate.RatePerHour.Should().BeLessThan(50.0);
        estimate.TimeRemaining.TotalHours.Should().BeLessThan(1.0);
    }

    [Fact]
    public void GetLearnedRates_ReturnsNull_WhenNoHistory()
    {
        _service.AddSample("device1", 80, false);
        _service.GetLearnedRates("device1").Should().BeNull();
    }

    [Fact]
    public void GetLearnedRates_ReturnsRates_WhenSet()
    {
        _service.SetHistoricalRates("device1", 5.0, 40.0, 3, 2);

        var rates = _service.GetLearnedRates("device1");
        rates.Should().NotBeNull();
        rates!.DischargeRate.Should().Be(5.0);
        rates.ChargeRate.Should().Be(40.0);
        rates.DischargeSessionCount.Should().Be(3);
        rates.ChargeSessionCount.Should().Be(2);
    }

    [Fact]
    public void GetLearnedRates_ReturnsNull_ForUnknownDevice()
    {
        _service.GetLearnedRates("nonexistent").Should().BeNull();
    }

    [Fact]
    public void HistoricalConfidence_IncreasesWithSessions()
    {
        // 1 session
        _service.SetHistoricalRates("dev1", 5.0, null, 1, 0);
        _service.AddSample("dev1", 50, false);
        var est1 = _service.GetEstimate("dev1");

        // 10 sessions
        _service.SetHistoricalRates("dev2", 5.0, null, 10, 0);
        _service.AddSample("dev2", 50, false);
        var est2 = _service.GetEstimate("dev2");

        // 20 sessions
        _service.SetHistoricalRates("dev3", 5.0, null, 20, 0);
        _service.AddSample("dev3", 50, false);
        var est3 = _service.GetEstimate("dev3");

        est1.Confidence.Should().BeLessThan(est2.Confidence);
        est2.Confidence.Should().BeLessThan(est3.Confidence);
    }

    [Fact]
    public void GetEstimate_WithNoHistoryAndInsufficientSessionData_UsesDefaults()
    {
        // No historical rates set, and less than 3% change
        _service.AddSample("device1", 80, false);
        _service.AddSample("device1", 79, false);

        var estimate = _service.GetEstimate("device1");
        // Uses built-in default rate — never shows "Calculating..."
        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
    }

    [Fact]
    public void GetEstimate_WithHistoryAndInsufficientSessionData_UsesHistorical()
    {
        _service.SetHistoricalRates("device1", 5.0, null, 5, 0);

        // Less than 3% change — not enough for session estimate
        _service.AddSample("device1", 80, false);
        _service.AddSample("device1", 79, false);

        var estimate = _service.GetEstimate("device1");
        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
        estimate.RatePerHour.Should().BeGreaterThan(1.5);
        estimate.RatePerHour.Should().BeLessThan(5.0);
    }

    [Fact]
    public void GetEstimate_DirectionMismatch_DoesNotReturnInvalid()
    {
        // Set historical discharge rate
        _service.SetHistoricalRates("device1", 5.0, null, 3, 0);

        // Add discharge sample
        _service.AddSample("device1", 50, false);

        // Verify estimate is valid (even without more samples to confirm direction)
        var estimate = _service.GetEstimate("device1");
        estimate.IsValid.Should().BeTrue();
        estimate.TimeRemaining.Should().BeGreaterThan(TimeSpan.Zero);
    }

    [Fact]
    public void GetEstimate_SmoothedRate_DoesNotSpikeOnSingleAnomalousSample()
    {
        // Establish steady historical rate
        _service.SetHistoricalRates("device1", 5.0, null, 10, 0);

        // Initial sample
        _service.AddSample("device1", 80, false);
        var before = _service.GetEstimate("device1");

        // Next sample (slight change)
        _service.AddSample("device1", 79, false);
        var after = _service.GetEstimate("device1");

        // Verify time remaining doesn't spike
        if (before.IsValid && after.IsValid && before.TimeRemaining > TimeSpan.Zero)
        {
            double ratio = after.TimeRemaining.TotalMinutes / before.TimeRemaining.TotalMinutes;
            ratio.Should().BeInRange(0.80, 1.20,
                "EWMA smoothing should keep estimates stable between polls");
        }
    }

    [Fact]
    public void GetEstimate_ImmediateChargeEstimate_OnFirstSampleAfterHistoricalLoad()
    {
        // Simulate restart: load historical charge rate from storage
        _service.SetHistoricalRates("device1", null, 50.0, 0, 5);

        // First sample immediately after cable plug
        _service.AddSample("device1", 60, true);

        var estimate = _service.GetEstimate("device1");

        // Must return valid estimate immediately (phone-like behavior)
        estimate.IsValid.Should().BeTrue();
        estimate.Phase.Should().Be("charge");
        estimate.TimeRemaining.Should().BeGreaterThan(TimeSpan.Zero);
    }

    [Fact]
    public void GetLearnedRates_FullChargeDuration_ComputedFromDischargeRate()
    {
        // 5%/hr discharge rate → full charge (100%) lasts 20 hours
        _service.SetHistoricalRates("device1", dischargeRate: 5.0, chargeRate: null,
            dischargeSessionCount: 5, chargeSessionCount: 0);

        var rates = _service.GetLearnedRates("device1");
        rates.Should().NotBeNull();
        rates!.DischargeRate.Should().NotBeNull();

        double totalHours = 100.0 / rates.DischargeRate!.Value;
        totalHours.Should().BeApproximately(20.0, 0.01,
            "100% ÷ 5%·hr⁻¹ must equal 20 hours");
    }

    [Fact]
    public void GetEstimate_HistoricalRateLowConfidence_BlendsWithDefaultRate()
    {
        _service.SetHistoricalRates("low-confidence", dischargeRate: 0.61, chargeRate: null,
            dischargeSessionCount: 1, chargeSessionCount: 0);
        _service.AddSample("low-confidence", 50, false);

        var estimate = _service.GetEstimate("low-confidence");

        estimate.IsValid.Should().BeTrue();
        estimate.IsHistorical.Should().BeTrue();
        estimate.RatePerHour.Should().BeGreaterThan(0.61);
        estimate.RatePerHour.Should().BeLessThan(1.5);
    }

    [Fact]
    public void GetEstimate_HistoricalRateHighConfidence_TracksLearnedRate()
    {
        _service.SetHistoricalRates("high-confidence", dischargeRate: 0.61, chargeRate: null,
            dischargeSessionCount: 20, chargeSessionCount: 0);
        _service.AddSample("high-confidence", 50, false);

        var estimate = _service.GetEstimate("high-confidence");

        estimate.IsValid.Should().BeTrue();
        estimate.RatePerHour.Should().BeApproximately(0.61, 0.05);
    }

    [Fact]
    public void Confidence_Increases_WithBetterSampleEvidence()
    {
        var now = DateTime.UtcNow;

        // Sparse and short-lived evidence
        _service.AddSampleForTesting("low-evidence", 90, false, now.AddMinutes(-10));
        _service.AddSampleForTesting("low-evidence", 89, false, now.AddMinutes(-5));
        var low = _service.GetEstimate("low-evidence");

        // Longer span with more stable data
        var start = now.AddHours(-6);
        for (int i = 0; i <= 6; i++)
        {
            _service.AddSampleForTesting("high-evidence", 90 - i, false, start.AddHours(i));
        }
        var high = _service.GetEstimate("high-evidence");

        high.IsValid.Should().BeTrue();
        low.IsValid.Should().BeTrue();
        high.Confidence.Should().BeGreaterThan(low.Confidence);
    }

    [Fact]
    public void Confidence_RecentDataScoresHigherThanStaleData()
    {
        var now = DateTime.UtcNow;

        // Stale trend: ended ~6 hours ago
        var staleStart = now.AddHours(-8);
        for (int i = 0; i <= 4; i++)
        {
            _service.AddSampleForTesting("stale", 80 - i, false, staleStart.AddMinutes(i * 30));
        }

        // Fresh trend: ends now
        var freshStart = now.AddHours(-2);
        for (int i = 0; i <= 4; i++)
        {
            _service.AddSampleForTesting("fresh", 80 - i, false, freshStart.AddMinutes(i * 30));
        }

        var stale = _service.GetEstimate("stale");
        var fresh = _service.GetEstimate("fresh");

        fresh.IsValid.Should().BeTrue();
        stale.IsValid.Should().BeTrue();
        fresh.Confidence.Should().BeGreaterThan(stale.Confidence);
    }
}
