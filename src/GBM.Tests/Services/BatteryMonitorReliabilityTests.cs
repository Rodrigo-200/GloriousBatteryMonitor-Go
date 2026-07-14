using System.Reflection;
using FluentAssertions;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Moq;
using Xunit;

namespace GBM.Tests.Services;

public class BatteryMonitorReliabilityTests
{
    [Fact]
    public void ProcessFailedRead_DoesNotReconnect_WhenRecentSuccessIsWithinWindow()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastSuccessfulReadUtc", DateTime.UtcNow);

        hid.Setup(s => s.IsDevicePresent(It.IsAny<DeviceProfile>())).Returns(true);
        hid.Setup(s => s.EnumerateDevices()).Returns(new List<DeviceInfo>
        {
            new()
            {
                VendorId = 0x093A,
                ProductId = 0x824D,
                ReleaseNumber = 1,
                IsWireless = true,
                DevicePath = "trigger",
                ModelName = "Model D 2 Wireless"
            }
        });

        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().NotBeNull();
    }

    [Fact]
    public void ProcessFailedRead_WhenTelemetryIsStaleButDevicePresent_TransitionsToLastKnown()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);

        InvokePrivate(monitor, "ProcessSuccessfulRead", 62, false, false);
        SetPrivateField(monitor, "_lastSuccessfulReadUtc", DateTime.UtcNow - TimeSpan.FromMinutes(10));

        hid.Setup(s => s.IsDevicePresent(It.IsAny<DeviceProfile>())).Returns(true);
        hid.Setup(s => s.EnumerateDevices()).Returns(new List<DeviceInfo>
        {
            new()
            {
                VendorId = 0x093A,
                ProductId = 0x824D,
                ReleaseNumber = 1,
                IsWireless = true,
                DevicePath = "trigger",
                ModelName = "Model D 2 Wireless"
            }
        });

        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");

        monitor.CurrentState.Connection.Should().Be(ConnectionState.LastKnown);
        monitor.CurrentState.Level.Should().Be(62);
        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeNull();
    }

    [Fact]
    public void ProcessFailedRead_ReconnectRequiresAbsenceEvidence_AndRespectsCooldown()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile();

        hid.Setup(s => s.IsDevicePresent(It.IsAny<DeviceProfile>())).Returns(false);

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastSuccessfulReadUtc", DateTime.UtcNow - TimeSpan.FromMinutes(10));

        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeNull();

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastSuccessfulReadUtc", DateTime.UtcNow - TimeSpan.FromMinutes(10));
        SetPrivateField(monitor, "_lastReconnectTriggerUtc", DateTime.UtcNow);

        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().NotBeNull();
    }

    [Fact]
    public void ProcessSuccessfulRead_PersistsLearnedRatesPeriodically_WithoutPhaseChange()
    {
        var monitor = CreateMonitor(out _, out _, out var storage, out var estimation);
        var profile = CreateProfile();

        estimation
            .Setup(e => e.GetLearnedRates(It.IsAny<string>()))
            .Returns(new LearnedRates(0.61, 40.0, 8, 3));

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastLearnedRatesPersistUtc", DateTime.UtcNow - TimeSpan.FromMinutes(10));

        InvokePrivate(monitor, "ProcessSuccessfulRead", 80, false, false);

        storage.Verify(
            s => s.UpdateLearnedRates(
                profile.CompositeKey,
                0.61,
                40.0,
                8,
                3,
                false),
            Times.Once);
    }

    [Fact]
    public void FailFailSuccessCadence_DoesNotDropActiveProfile()
    {
        var monitor = CreateMonitor(out _, out _, out _, out _);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);

        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessSuccessfulRead", 78, false, false);

        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessFailedRead");
        InvokePrivate(monitor, "ProcessSuccessfulRead", 77, false, false);

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().NotBeNull();
    }

    [Fact]
    public void SleepAndWakeTransitions_DoNotFeedEstimatorSamples()
    {
        var monitor = CreateMonitor(out _, out _, out _, out var estimation);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastPositiveLevel", 75);

        InvokePrivate(monitor, "ProcessSuccessfulRead", 0, false, false);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 0, false, false);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 0, false, false);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 74, false, false);

        estimation.Verify(
            e => e.AddSample(It.IsAny<string>(), It.IsAny<int>(), It.IsAny<bool>()),
            Times.Never);
    }

    [Fact]
    public async Task PollOnceAsync_WhenWiredAndRealtimeReadIsPlausible_UsesRealtimeLevel()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastPositiveLevel", 64);

        hid.Setup(s => s.IsWiredDevicePresent(profile.ModelName)).Returns(true);
        hid.Setup(s => s.ReadBattery(It.Is<DeviceProfile>(p => p == profile)))
            .Returns((true, 67, true));

        await InvokePrivateAsync(monitor, "PollOnceAsync", CancellationToken.None);

        monitor.CurrentState.Level.Should().Be(67);
        monitor.CurrentState.IsCharging.Should().BeTrue();

        hid.Verify(s => s.ReadBattery(It.Is<DeviceProfile>(p => p == profile)), Times.Once);
    }

    [Fact]
    public async Task PollOnceAsync_WhenWiredAndRealtimeReadLooksSuspicious_FallsBackToEstimate()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastWiredPresent", true);
        SetPrivateField(monitor, "_lastPositiveLevel", 70);
        SetPrivateField(monitor, "_chargeStartTime", null);
        SetPrivateField(monitor, "_chargeStartLevel", 0);

        hid.Setup(s => s.IsWiredDevicePresent(profile.ModelName)).Returns(true);
        hid.Setup(s => s.ReadBattery(It.Is<DeviceProfile>(p => p == profile)))
            .Returns((true, 8, false));

        await InvokePrivateAsync(monitor, "PollOnceAsync", CancellationToken.None);

        monitor.CurrentState.Level.Should().Be(70);
        monitor.CurrentState.IsCharging.Should().BeTrue();
    }

    [Fact]
    public void ProcessSuccessfulRead_FirstStableReadAfterUnplug_LearnsCalibration()
    {
        var monitor = CreateMonitor(out _, out _, out _, out var estimation);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_awaitingUnplugCalibrationSample", true);
        SetPrivateField(monitor, "_pendingUnplugCalibrationAnchorLevel", 100);
        SetPrivateField(monitor, "_pendingUnplugCalibrationUtc", DateTime.UtcNow - TimeSpan.FromSeconds(20));

        InvokePrivate(monitor, "ProcessSuccessfulRead", 86, false, false);

        estimation.Verify(
            e => e.ObserveChargeDropAfterUnplug(profile.CompositeKey, 100, 86, It.IsAny<TimeSpan>()),
            Times.Once);
    }

    [Fact]
    public void ProcessSuccessfulRead_NonChargingHigherLevel_PromotesChargedToLevel()
    {
        var monitor = CreateMonitor(out _, out _, out var storage, out _);
        var profile = CreateProfile();

        SetPrivateField(monitor, "_activeProfile", profile);
        storage.Setup(s => s.GetDeviceChargeData(profile.CompositeKey)).Returns(new DeviceChargeData
        {
            CompositeKey = profile.CompositeKey,
            LastKnownLevel = 75,
            LastReadTime = DateTime.UtcNow.AddMinutes(-10),
            LastChargeTime = DateTime.UtcNow.AddMinutes(-10),
            LastChargeLevel = 75
        });

        InvokePrivate(monitor, "ProcessSuccessfulRead", 100, false, false);

        monitor.CurrentState.Level.Should().Be(100);
        monitor.CurrentState.IsCharging.Should().BeFalse();
        monitor.CurrentState.LastChargeLevel.Should().Be(100);
        monitor.CurrentState.LastChargeTime.Should().NotBeNull();
    }

    [Fact]
    public void ProcessFailedRead_CandidateG_UsesHigherFailureThresholdBeforeReconnect()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateG);

        hid.Setup(s => s.IsDevicePresent(It.IsAny<DeviceProfile>())).Returns(false);
        SetPrivateField(monitor, "_activeProfile", profile);
        SetPrivateField(monitor, "_lastSuccessfulReadUtc", DateTime.UtcNow - TimeSpan.FromMinutes(10));

        for (int i = 0; i < 9; i++)
        {
            InvokePrivate(monitor, "ProcessFailedRead");
        }

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().NotBeNull();

        InvokePrivate(monitor, "ProcessFailedRead");
        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeNull();
    }

    [Fact]
    public void TryRestoreCachedProfile_CandidateGRequiresSiblingInputInterface()
    {
        var monitor = CreateMonitor(out var hid, out _, out var storage, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateG);

        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile> { profile });
        hid.Setup(s => s.IsDevicePresent(It.Is<DeviceProfile>(p => p.DevicePath == profile.DevicePath)))
            .Returns(true);
        hid.Setup(s => s.IsDevicePresent(It.Is<DeviceProfile>(p => p.DevicePath == profile.SiblingDevicePath)))
            .Returns(false);
        hid.Setup(s => s.ReadBattery(profile)).Returns((false, 0, false));

        InvokePrivate(monitor, "TryRestoreCachedProfile");

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeNull();
    }

    [Fact]
    public void TryRestoreCachedProfile_PixartProfileRequiresSuccessfulRead()
    {
        var monitor = CreateMonitor(out var hid, out _, out var storage, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateG);

        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile> { profile });
        hid.Setup(s => s.IsDevicePresent(It.IsAny<DeviceProfile>())).Returns(true);
        hid.Setup(s => s.ReadBattery(profile)).Returns((false, 0, false));

        InvokePrivate(monitor, "TryRestoreCachedProfile");

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeNull();
    }

    [Fact]
    public void TryRestoreCachedProfile_SuccessfulReadUpdatesCurrentState()
    {
        var monitor = CreateMonitor(out var hid, out _, out var storage, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateF);

        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile> { profile });
        hid.Setup(s => s.IsDevicePresent(profile)).Returns(true);
        hid.Setup(s => s.ReadBattery(profile)).Returns((true, 99, false));

        InvokePrivate(monitor, "TryRestoreCachedProfile");

        monitor.CurrentState.Connection.Should().Be(ConnectionState.Connected);
        monitor.CurrentState.Level.Should().Be(99);
        hid.Verify(s => s.ReadBattery(profile), Times.Once);
    }

    [Fact]
    public async Task TriggerRescan_DuringWiredRead_DoesNotCorruptState()
    {
        var monitor = CreateMonitor(out var hid, out _, out _, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateG);
        using var readStarted = new ManualResetEventSlim();
        using var releaseRead = new ManualResetEventSlim();

        SetPrivateField(monitor, "_activeProfile", profile);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 96, false, false);
        SetPrivateField(monitor, "_lastWiredPresent", true);
        SetPrivateField(monitor, "_chargeStartTime", DateTime.UtcNow);
        SetPrivateField(monitor, "_chargeStartLevel", 96);

        hid.Setup(s => s.IsWiredDevicePresent(profile.ModelName)).Returns(true);
        hid.Setup(s => s.ReadBattery(profile)).Returns(() =>
        {
            readStarted.Set();
            releaseRead.Wait(TimeSpan.FromSeconds(5)).Should().BeTrue();
            return (false, 0, false);
        });

        Task poll = Task.Run(() => InvokePrivateAsync(monitor, "PollOnceAsync", CancellationToken.None));
        readStarted.Wait(TimeSpan.FromSeconds(5)).Should().BeTrue();
        monitor.TriggerRescan();
        releaseRead.Set();
        await poll;

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeSameAs(profile);
        GetPrivateField<int>(monitor, "_lastPositiveLevel").Should().Be(96);
        GetPrivateField<int>(monitor, "_chargeStartLevel").Should().Be(96);
        monitor.CurrentState.Level.Should().Be(96);
        monitor.CurrentState.DeviceName.Should().Be(profile.ModelName);
        monitor.CurrentState.IsCharging.Should().BeTrue();
    }

    [Fact]
    public async Task PollOnceAsync_ManualRescanMissPreservesWorkingWirelessProfileAndState()
    {
        var monitor = CreateMonitor(out var hid, out _, out var storage, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateA);
        var publishedConnections = new List<ConnectionState>();

        SetPrivateField(monitor, "_activeProfile", profile);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 97, false, false);
        monitor.BatteryStateChanged += state => publishedConnections.Add(state.Connection);

        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile> { profile });
        hid.Setup(s => s.IsDevicePresent(profile)).Returns(true);
        hid.Setup(s => s.ReadBattery(profile)).Returns((false, 0, false));
        hid.Setup(s => s.EnumerateDevices()).Returns(new List<DeviceInfo>());

        monitor.TriggerRescan();
        await InvokePrivateAsync(monitor, "PollOnceAsync", CancellationToken.None);

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeSameAs(profile);
        monitor.CurrentState.Connection.Should().Be(ConnectionState.Connected);
        monitor.CurrentState.Level.Should().Be(97);
        publishedConnections.Should().NotContain(ConnectionState.Connecting);
        publishedConnections.Should().NotContain(ConnectionState.NotConnected);
    }

    [Fact]
    public async Task PollOnceAsync_ManualRescanToDifferentHidPathsResetsDeviceTracking()
    {
        var monitor = CreateMonitor(out var hid, out _, out var storage, out var estimation);
        var original = CreateProfile(PixartBatteryMethod.CandidateA);
        var replacement = CreateProfile(PixartBatteryMethod.CandidateA);
        replacement.DevicePath = "replacement-trigger";
        replacement.SiblingDevicePath = "replacement-input";

        SetPrivateField(monitor, "_activeProfile", original);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 97, true, false);
        SetPrivateField(monitor, "_lastWiredPresent", true);
        SetPrivateField(monitor, "_chargeStartTime", DateTime.UtcNow);
        SetPrivateField(monitor, "_chargeStartLevel", 97);
        SetPrivateField(monitor, "_awaitingUnplugCalibrationSample", true);

        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile> { replacement });
        hid.Setup(s => s.IsDevicePresent(replacement)).Returns(true);
        hid.Setup(s => s.ReadBattery(replacement)).Returns((true, 55, false));

        monitor.TriggerRescan();
        await InvokePrivateAsync(monitor, "PollOnceAsync", CancellationToken.None);

        GetPrivateField<DeviceProfile?>(monitor, "_activeProfile").Should().BeSameAs(replacement);
        GetPrivateField<int>(monitor, "_lastPositiveLevel").Should().Be(55);
        GetPrivateField<int>(monitor, "_chargeStartLevel").Should().Be(0);
        GetPrivateField<bool>(monitor, "_lastWiredPresent").Should().BeFalse();
        GetPrivateField<bool>(monitor, "_awaitingUnplugCalibrationSample").Should().BeFalse();
        monitor.CurrentState.Level.Should().Be(55);
        estimation.Verify(e => e.Reset(original.CompositeKey), Times.Once);
    }

    [Fact]
    public void ActivateProfile_DifferentHidPathsAfterDisconnectResetsPriorDeviceTracking()
    {
        var monitor = CreateMonitor(out _, out _, out _, out var estimation);
        var original = CreateProfile(PixartBatteryMethod.CandidateA);
        var replacement = CreateProfile(PixartBatteryMethod.CandidateA);
        replacement.DevicePath = "replacement-trigger";
        replacement.SiblingDevicePath = "replacement-input";

        InvokePrivate(monitor, "ActivateProfile", original);
        InvokePrivate(monitor, "ProcessSuccessfulRead", 97, true, false);
        SetPrivateField(monitor, "_lastWiredPresent", true);
        SetPrivateField(monitor, "_chargeStartLevel", 97);
        SetPrivateField(monitor, "_awaitingUnplugCalibrationSample", true);
        SetPrivateField(monitor, "_activeProfile", null);

        InvokePrivate(monitor, "ActivateProfile", replacement);

        GetPrivateField<int>(monitor, "_lastPositiveLevel").Should().Be(0);
        GetPrivateField<int>(monitor, "_chargeStartLevel").Should().Be(0);
        GetPrivateField<bool>(monitor, "_lastWiredPresent").Should().BeFalse();
        GetPrivateField<bool>(monitor, "_awaitingUnplugCalibrationSample").Should().BeFalse();
        estimation.Verify(e => e.Reset(original.CompositeKey), Times.Once);
    }

    [Fact]
    public void AttemptDeviceDiscovery_SuccessfulCachedReadUpdatesCurrentState()
    {
        var monitor = CreateMonitor(out var hid, out _, out var storage, out _);
        var profile = CreateProfile(PixartBatteryMethod.CandidateF);

        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile> { profile });
        hid.Setup(s => s.ReadBattery(profile)).Returns((true, 98, false));

        InvokePrivate(monitor, "AttemptDeviceDiscovery");

        monitor.CurrentState.Connection.Should().Be(ConnectionState.Connected);
        monitor.CurrentState.Level.Should().Be(98);
        hid.Verify(s => s.ReadBattery(profile), Times.Once);
    }

    private static BatteryMonitorService CreateMonitor(
        out Mock<IHidDeviceService> hid,
        out Mock<ISettingsService> settings,
        out Mock<IStorageService> storage,
        out Mock<IBatteryEstimationService> estimation)
    {
        var logger = new Mock<ILogger<BatteryMonitorService>>();
        hid = new Mock<IHidDeviceService>();
        settings = new Mock<ISettingsService>();
        storage = new Mock<IStorageService>();
        estimation = new Mock<IBatteryEstimationService>();
        var notification = new Mock<INotificationService>();

        settings.SetupGet(s => s.Current).Returns(new AppSettings
        {
            CriticalBatteryThreshold = 10,
            RefreshIntervalSeconds = 5
        });

        storage.Setup(s => s.GetDeviceChargeData(It.IsAny<string>())).Returns((DeviceChargeData?)null);
        storage.Setup(s => s.LoadProfiles()).Returns(new List<DeviceProfile>());
        storage.Setup(s => s.LoadChargeData()).Returns(new ChargeData());

        estimation.Setup(e => e.GetEstimate(It.IsAny<string>())).Returns(new BatteryEstimate
        {
            IsValid = true,
            TimeRemaining = TimeSpan.FromHours(4),
            Confidence = 0.5,
            Phase = "discharge",
            RatePerHour = 1.0,
            SampleCount = 1,
            IsHistorical = true
        });
        estimation.Setup(e => e.GetLearnedRates(It.IsAny<string>())).Returns((LearnedRates?)null);
        estimation.Setup(e => e.GetChargeCalibration(It.IsAny<string>())).Returns((ChargeCalibration?)null);

        return new BatteryMonitorService(
            logger.Object,
            hid.Object,
            settings.Object,
            storage.Object,
            estimation.Object,
            notification.Object);
    }

    private static DeviceProfile CreateProfile(PixartBatteryMethod method = PixartBatteryMethod.CandidateA)
    {
        return new DeviceProfile
        {
            CompositeKey = "093A_824D_1_True",
            DevicePath = "trigger",
            SiblingDevicePath = "input",
            VendorId = 0x093A,
            ProductId = 0x824D,
            ModelName = "Model D 2 Wireless",
            Protocol = ChipProtocol.Pixart,
            PixartMethod = method,
            ReportId = 0,
            ReportLength = 64,
            UseFeatureReports = true,
            LastSeen = DateTime.UtcNow
        };
    }

    private static void InvokePrivate(object target, string methodName, params object[] args)
    {
        var method = target.GetType().GetMethod(methodName, BindingFlags.Instance | BindingFlags.NonPublic);
        method.Should().NotBeNull($"Expected private method {methodName} to exist");
        method!.Invoke(target, args);
    }

    private static async Task InvokePrivateAsync(object target, string methodName, params object[] args)
    {
        var method = target.GetType().GetMethod(methodName, BindingFlags.Instance | BindingFlags.NonPublic);
        method.Should().NotBeNull($"Expected private method {methodName} to exist");
        var result = method!.Invoke(target, args);
        if (result is Task task)
        {
            await task.ConfigureAwait(false);
        }
    }

    private static T GetPrivateField<T>(object target, string fieldName)
    {
        var field = target.GetType().GetField(fieldName, BindingFlags.Instance | BindingFlags.NonPublic);
        field.Should().NotBeNull($"Expected private field {fieldName} to exist");
        return (T)field!.GetValue(target)!;
    }

    private static void SetPrivateField(object target, string fieldName, object? value)
    {
        var field = target.GetType().GetField(fieldName, BindingFlags.Instance | BindingFlags.NonPublic);
        field.Should().NotBeNull($"Expected private field {fieldName} to exist");
        field!.SetValue(target, value);
    }
}
