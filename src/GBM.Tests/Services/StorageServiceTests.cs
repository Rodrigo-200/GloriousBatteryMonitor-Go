using FluentAssertions;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Moq;
using Xunit;

namespace GBM.Tests.Services;

public class StorageServiceTests
{
    [Fact]
    public void AddBatterySample_WhenCharging_UpdatesLastChargeFields()
    {
        string appDataPath = CreateTempDirectory();
        try
        {
            var service = CreateStorageService(appDataPath);

            service.AddBatterySample("device", 55, isCharging: true);

            var device = service.GetDeviceChargeData("device");
            device.Should().NotBeNull();
            device!.LastKnownLevel.Should().Be(55);
            device.LastChargeLevel.Should().Be(55);
            device.LastChargeTime.Should().NotBeNull();
            device.Samples.Should().ContainSingle();
            device.Samples[0].IsCharging.Should().BeTrue();
            device.Samples[0].Level.Should().Be(55);
        }
        finally
        {
            TryDeleteDirectory(appDataPath);
        }
    }

    [Fact]
    public void UpdateLearnedRates_ForceFlagControlsImmediateDiskPersistence()
    {
        string appDataPath = CreateTempDirectory();
        const string deviceKey = "device";

        try
        {
            var service = CreateStorageService(appDataPath);
            service.AddBatterySample(deviceKey, 60, isCharging: false);

            // Non-forced update should be throttled when called immediately after a save.
            service.UpdateLearnedRates(deviceKey, 0.4, 45.0, 2, 3, forceSave: false);

            var reloadedAfterNonForced = CreateStorageService(appDataPath).GetDeviceChargeData(deviceKey);
            reloadedAfterNonForced.Should().NotBeNull();
            reloadedAfterNonForced!.LearnedDischargeRate.Should().BeNull();
            reloadedAfterNonForced.LearnedChargeRate.Should().BeNull();
            reloadedAfterNonForced.DischargeSessionCount.Should().Be(0);
            reloadedAfterNonForced.ChargeSessionCount.Should().Be(0);

            // Forced update should persist immediately, even within the throttle window.
            service.UpdateLearnedRates(deviceKey, 0.4, 45.0, 2, 3, forceSave: true);

            var reloadedAfterForced = CreateStorageService(appDataPath).GetDeviceChargeData(deviceKey);
            reloadedAfterForced.Should().NotBeNull();
            reloadedAfterForced!.LearnedDischargeRate.Should().Be(0.4);
            reloadedAfterForced.LearnedChargeRate.Should().Be(45.0);
            reloadedAfterForced.DischargeSessionCount.Should().Be(2);
            reloadedAfterForced.ChargeSessionCount.Should().Be(3);
        }
        finally
        {
            TryDeleteDirectory(appDataPath);
        }
    }

    [Fact]
    public void UpdateChargeCalibration_ForceFlagControlsImmediateDiskPersistence()
    {
        string appDataPath = CreateTempDirectory();
        const string deviceKey = "device";

        try
        {
            var service = CreateStorageService(appDataPath);
            service.AddBatterySample(deviceKey, 75, isCharging: true);

            service.UpdateChargeCalibration(deviceKey, 12.0, 2, forceSave: false);

            var reloadedAfterNonForced = CreateStorageService(appDataPath).GetDeviceChargeData(deviceKey);
            reloadedAfterNonForced.Should().NotBeNull();
            reloadedAfterNonForced!.LearnedChargeOvershootPercent.Should().BeNull();
            reloadedAfterNonForced.ChargeOvershootObservationCount.Should().Be(0);

            service.UpdateChargeCalibration(deviceKey, 12.0, 2, forceSave: true);

            var reloadedAfterForced = CreateStorageService(appDataPath).GetDeviceChargeData(deviceKey);
            reloadedAfterForced.Should().NotBeNull();
            reloadedAfterForced!.LearnedChargeOvershootPercent.Should().Be(12.0);
            reloadedAfterForced.ChargeOvershootObservationCount.Should().Be(2);
        }
        finally
        {
            TryDeleteDirectory(appDataPath);
        }
    }

    private static StorageService CreateStorageService(string appDataPath)
    {
        var logger = new Mock<ILogger<StorageService>>();
        var settings = new Mock<ISettingsService>();

        settings.SetupGet(s => s.Current).Returns(new AppSettings());
        settings.Setup(s => s.GetAppDataPath()).Returns(appDataPath);

        return new StorageService(logger.Object, settings.Object);
    }

    private static string CreateTempDirectory()
    {
        string dir = Path.Combine(Path.GetTempPath(), "GBM.Tests", Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(dir);
        return dir;
    }

    private static void TryDeleteDirectory(string dir)
    {
        try
        {
            if (Directory.Exists(dir))
                Directory.Delete(dir, recursive: true);
        }
        catch
        {
            // Best-effort cleanup for test temp directories.
        }
    }
}
