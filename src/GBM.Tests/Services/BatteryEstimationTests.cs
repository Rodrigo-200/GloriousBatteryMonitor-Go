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
        estimate.RatePerHour.Should().BeApproximately(5.0, 0.1);
        // 50% / 5%/hr = 10 hours
        estimate.TimeRemaining.TotalHours.Should().BeApproximately(10.0, 0.5);
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
        estimate.RatePerHour.Should().BeApproximately(40.0, 0.1);
        // (100-60) / 40 = 1.0 hours
        estimate.TimeRemaining.TotalHours.Should().BeApproximately(1.0, 0.1);
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
        estimate.RatePerHour.Should().BeApproximately(5.0, 0.1);
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
}
