using FluentAssertions;
using GBM.Core.Models;
using GBM.Core.Services;
using GBM.Desktop.ViewModels;
using Moq;
using Xunit;

namespace GBM.Tests.ViewModels;

public class MainViewModelTests
{
    [Fact]
    public void AppSettings_Clone_CreatesIndependentCopy()
    {
        var original = new AppSettings
        {
            StartWithOS = true,
            RefreshIntervalSeconds = 10,
            LowBatteryThreshold = 25,
            Theme = "dark"
        };

        var clone = original.Clone();
        clone.StartWithOS.Should().Be(original.StartWithOS);
        clone.RefreshIntervalSeconds.Should().Be(original.RefreshIntervalSeconds);
        clone.LowBatteryThreshold.Should().Be(original.LowBatteryThreshold);
        clone.Theme.Should().Be(original.Theme);

        // Modify clone, original should not change
        clone.StartWithOS = false;
        original.StartWithOS.Should().BeTrue();
    }

    [Fact]
    public void AppSettings_DefaultValues_AreCorrect()
    {
        var settings = new AppSettings();
        settings.StartWithOS.Should().BeFalse();
        settings.StartMinimized.Should().BeFalse();
        settings.RefreshIntervalSeconds.Should().Be(5);
        settings.NotificationsEnabled.Should().BeTrue();
        settings.LowBatteryThreshold.Should().Be(20);
        settings.CriticalBatteryThreshold.Should().Be(10);
        settings.ShowPercentageOnTrayIcon.Should().BeFalse();
        settings.EnableBetaUpdates.Should().BeFalse();
        settings.HasSetBetaChannelPreference.Should().BeFalse();
        settings.Theme.Should().Be("system");
    }

    [Fact]
    public void BatteryEstimate_Invalid_HasDefaultValues()
    {
        var estimate = BatteryEstimate.Invalid;
        estimate.IsValid.Should().BeFalse();
        estimate.TimeRemaining.Should().Be(TimeSpan.Zero);
        estimate.Confidence.Should().Be(0);
        estimate.RatePerHour.Should().Be(0);
        estimate.SampleCount.Should().Be(0);
    }

    [Fact]
    public void DeviceProfile_Properties_SetCorrectly()
    {
        var profile = new DeviceProfile
        {
            CompositeKey = "test_key",
            DevicePath = "/dev/hidraw0",
            ReportId = 0x04,
            ReportLength = 64,
            UseFeatureReports = true,
            VendorId = 0x258A,
            ProductId = 0x2012,
            ModelName = "Model D"
        };

        profile.CompositeKey.Should().Be("test_key");
        profile.ReportId.Should().Be(0x04);
        profile.UseFeatureReports.Should().BeTrue();
        profile.ModelName.Should().Be("Model D");
    }

    [Fact]
    public void ChargeData_DefaultsToEmptyDevices()
    {
        var data = new ChargeData();
        data.Devices.Should().NotBeNull();
        data.Devices.Should().BeEmpty();
    }

    [Fact]
    public void DeviceChargeData_DefaultsToEmptySamples()
    {
        var data = new DeviceChargeData();
        data.Samples.Should().NotBeNull();
        data.Samples.Should().BeEmpty();
    }

    [Fact]
    public void NotificationType_HasAllExpectedValues()
    {
        Enum.GetValues<NotificationType>().Should().HaveCount(4);
        Enum.IsDefined(NotificationType.Low).Should().BeTrue();
        Enum.IsDefined(NotificationType.Critical).Should().BeTrue();
        Enum.IsDefined(NotificationType.FullCharge).Should().BeTrue();
        Enum.IsDefined(NotificationType.Disconnected).Should().BeTrue();
    }

    [Fact]
    public void AppSettings_NotificationCooldown_DefaultsToFive()
    {
        new AppSettings().NotificationCooldownMinutes.Should().Be(5);
    }

    [Fact]
    public void AppSettings_DebugLogging_DefaultsToFalse()
    {
        new AppSettings().DebugLogging.Should().BeFalse();
    }

    [Fact]
    public void AppSettings_Clone_IncludesNewFields()
    {
        var original = new AppSettings
        {
            NotificationCooldownMinutes = 15,
            DebugLogging = true,
            EnableBetaUpdates = true,
            HasSetBetaChannelPreference = true
        };
        var clone = original.Clone();
        clone.NotificationCooldownMinutes.Should().Be(15);
        clone.DebugLogging.Should().BeTrue();
        clone.EnableBetaUpdates.Should().BeTrue();
        clone.HasSetBetaChannelPreference.Should().BeTrue();
        clone.NotificationCooldownMinutes = 99;
        clone.EnableBetaUpdates = false;
        clone.HasSetBetaChannelPreference = false;
        original.NotificationCooldownMinutes.Should().Be(15);
        original.EnableBetaUpdates.Should().BeTrue();
        original.HasSetBetaChannelPreference.Should().BeTrue();
    }

    [Fact]
    public void SetThemeCommand_NormalizesTheme_AndUpdatesSelectionState()
    {
        var settingsService = new TestSettingsService(new AppSettings { Theme = "system" });
        var viewModel = CreateViewModel(settingsService);

        viewModel.SetThemeCommand.Execute(" LIGHT ");

        viewModel.CurrentTheme.Should().Be("light");
        viewModel.ThemeSelectedIndex.Should().Be(2);
        viewModel.IsLightThemeSelected.Should().BeTrue();
        viewModel.IsDarkThemeSelected.Should().BeFalse();
        viewModel.IsSystemThemeSelected.Should().BeFalse();
    }

    [Fact]
    public void CloseSettingsCommand_RevertsUnsavedThemePreview()
    {
        var settingsService = new TestSettingsService(new AppSettings { Theme = "dark" });
        var viewModel = CreateViewModel(settingsService);

        viewModel.OpenSettingsCommand.Execute(null);
        viewModel.SetThemeCommand.Execute("light");

        viewModel.CloseSettingsCommand.Execute(null);

        viewModel.CurrentTheme.Should().Be("dark");
        viewModel.ThemeSelectedIndex.Should().Be(1);
        viewModel.IsDarkThemeSelected.Should().BeTrue();
        viewModel.IsSettingsOpen.Should().BeFalse();
    }

    [Fact]
    public void SaveSettingsCommand_PersistsPreviewedTheme()
    {
        var settingsService = new TestSettingsService(new AppSettings { Theme = "system" });
        var viewModel = CreateViewModel(settingsService);

        viewModel.OpenSettingsCommand.Execute(null);
        viewModel.SetThemeCommand.Execute("dark");
        viewModel.SaveSettingsCommand.Execute(null);

        settingsService.Current.Theme.Should().Be("dark");
        viewModel.ThemeSelectedIndex.Should().Be(1);
        settingsService.SaveCallCount.Should().Be(1);
        viewModel.IsSettingsOpen.Should().BeFalse();
    }

    private static MainViewModel CreateViewModel(TestSettingsService settingsService)
    {
        var monitorService = new Mock<IBatteryMonitorService>();
        monitorService.SetupGet(x => x.CurrentState).Returns(BatteryState.Disconnected);
        monitorService.SetupGet(x => x.CurrentEstimate).Returns(BatteryEstimate.Invalid);
        monitorService.SetupGet(x => x.IsRunning).Returns(false);

        var hidService = new Mock<IHidDeviceService>();

        var autoStartService = new Mock<IAutoStartService>();
        var updateService = new Mock<IUpdateService>();
        updateService.SetupGet(x => x.CurrentVersion).Returns("3.3.0");
        updateService.Setup(x => x.IsUpdatePendingRestart()).Returns(false);
        updateService.Setup(x => x.CheckForUpdateAsync()).ReturnsAsync((UpdateCheckResult?)null);
        updateService.Setup(x => x.DownloadUpdateAsync(It.IsAny<IProgress<int>?>())).ReturnsAsync(false);

        var storageService = new Mock<IStorageService>();
        storageService.Setup(x => x.LoadChargeData()).Returns(new ChargeData());
        storageService.Setup(x => x.LoadProfiles()).Returns([]);

        var estimationService = new Mock<IBatteryEstimationService>();

        return new MainViewModel(
            monitorService.Object,
            settingsService,
            hidService.Object,
            autoStartService.Object,
            updateService.Object,
            storageService.Object,
            estimationService.Object);
    }

    private sealed class TestSettingsService : ISettingsService
    {
        private AppSettings _current;

        public TestSettingsService(AppSettings current)
        {
            _current = current.Clone();
        }

        public AppSettings Current => _current;
        public int SaveCallCount { get; private set; }

        public event Action<AppSettings>? SettingsChanged;

        public void Load()
        {
        }

        public void Save(AppSettings settings)
        {
            SaveCallCount++;
            _current = settings.Clone();
            SettingsChanged?.Invoke(_current);
        }

        public string GetAppDataPath()
        {
            return string.Empty;
        }
    }
}
