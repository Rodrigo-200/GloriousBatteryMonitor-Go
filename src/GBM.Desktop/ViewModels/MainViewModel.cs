using Avalonia.Threading;
using Avalonia;
using Avalonia.Media;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using GBM.Core.Models;
using GBM.Core.Services;
using GBM.Desktop.Services;

namespace GBM.Desktop.ViewModels;

public partial class MainViewModel : ViewModelBase, IDisposable
{
    private const double FullChargeDurationDisplayCapHours = 24.0 * 30.0;
    private static readonly TimeSpan FullChargeDurationRefreshInterval = TimeSpan.FromMinutes(1);
    private static readonly TimeSpan ToastDuration = TimeSpan.FromSeconds(3);
    private static readonly TimeSpan MinimumLastSyncUpdateInterval = TimeSpan.FromMilliseconds(250);

    private readonly IBatteryMonitorService _monitorService;
    private readonly ISettingsService _settingsService;
    private readonly IHidDeviceService _hidService;
    private readonly IAutoStartService _autoStartService;
    private readonly IUpdateService _updateService;
    private readonly IStorageService _storageService;
    private readonly IBatteryEstimationService _estimationService;
    private readonly BatteryAlertSoundService? _batteryAlertSoundService;
    private readonly MouseDpiMonitorService? _dpiMonitorService;
    private bool _betaDefaultAppliedThisSession;
    private readonly object _criticalSoundLock = new();
    private bool _criticalSoundFired;
    private string _criticalSoundDeviceName = string.Empty;
    private const string DefaultDeviceName = "Glorious Mouse";
    private DpiReading? _lastDpi;

    // Battery state properties
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(IsLowBattery))]
    private int _batteryLevel;
    [ObservableProperty] private string _batteryPercentText = "—";
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(IsLowBattery))]
    private bool _isCharging;
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(IsLowBattery))]
    private bool _isConnected;
    [ObservableProperty] private string _deviceName = DefaultDeviceName;
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(WidgetOfflineText), nameof(IsWidgetOffline), nameof(ShowDpiChip))]
    private ConnectionState _connectionState = ConnectionState.NotConnected;
    [ObservableProperty] private string _statusText = "Not Connected";

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowDpiChip))]
    private bool _isDpiReceiver;

    // DPI: only the Model O Pro receiver reports it. Until it does, the chip stays hidden.
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowDpiChip))]
    private bool _hasDpi;
    [ObservableProperty] private string _currentDpiText = string.Empty;
    [ObservableProperty] private IBrush _dpiStageBrush = new SolidColorBrush(Color.Parse("#929AA5"));
    [ObservableProperty] private string _dpiTooltip = string.Empty;

    // Estimation
    [ObservableProperty] private bool _hasEstimate;
    [ObservableProperty] private TimeSpan _timeRemaining;
    [ObservableProperty] private string _timeRemainingText = "—";
    [ObservableProperty] private string _estimateLabel = "";
    [ObservableProperty] private string _estimateTooltip = "";

    // Charge history
    [ObservableProperty] private string _lastChargedText = "—";
    [ObservableProperty] private string _chargedToText = "—";
    [ObservableProperty] private string _fullChargeDurationText = "—";

    // Last sync
    [ObservableProperty] private string _lastSyncText = "—";
    private DateTime? _lastReadTime;
    private DateTime _lastFullChargeDurationRefreshUtc;
    private string _lastFullChargeDurationDeviceName = string.Empty;

    // Settings overlay
    [ObservableProperty] private bool _isSettingsOpen;

    // Settings fields (bound to SettingsView controls)
    [ObservableProperty] private bool _startWithOS;
    [ObservableProperty] private bool _startMinimized;
    [ObservableProperty] private bool _notificationsEnabled;
    [ObservableProperty] private string _criticalBatterySoundPath = string.Empty;
    [ObservableProperty] private bool _showPercentageOnTray;
    [ObservableProperty] private bool _showBatteryWidget = true;
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowDpiChip))]
    private bool _showDpi = true;
    [ObservableProperty] private string _language = "system";
    [ObservableProperty] private int _languageSelectedIndex;
    [ObservableProperty] private int _refreshInterval = 5;
    [ObservableProperty] private int _lowThreshold = 20;
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(CriticalSoundDescription))]
    private int _criticalThreshold = 10;
    [ObservableProperty] private int _notificationCooldownMinutes = 5;
    [ObservableProperty] private bool _debugLogging;
    [ObservableProperty] private bool _enableBetaUpdates;
    [ObservableProperty] private bool _isAdvancedExpanded;
    [ObservableProperty] private bool _isDevToolsExpanded;

    // Developer tools output
    [ObservableProperty] private string _devOutput = string.Empty;

    // Theme
    [ObservableProperty] private string _currentTheme = "system";
    [ObservableProperty] private bool _isDarkTheme;

    // Update
    [ObservableProperty] private bool _updateAvailable;
    [ObservableProperty] private string _updateVersion = string.Empty;
    [ObservableProperty] private string _updateStatusText = string.Empty;
    [ObservableProperty] private bool _isCheckingUpdate = false;
    [ObservableProperty] private bool _isUpdateAvailable = false;
    [ObservableProperty] private int _updateDownloadProgress = 0;
    [ObservableProperty] private bool _isDownloadingUpdate = false;
    [ObservableProperty] private bool _isUpdateReadyToInstall = false;
    [ObservableProperty] private string _updateActionButtonText = "Download Update";
    [ObservableProperty] private bool _showUpdateIndicator = false;
    [ObservableProperty] private string _updateIndicatorText = string.Empty;
    [ObservableProperty] private string _updateIndicatorActionText = "Open";
    [ObservableProperty] private bool _showUpdateIndicatorAction = false;

    // Toast
    [ObservableProperty] private bool _isToastVisible;
    [ObservableProperty] private string _toastMessage = string.Empty;

    public MainViewModel(
        IBatteryMonitorService monitorService,
        ISettingsService settingsService,
        IHidDeviceService hidService,
        IAutoStartService autoStartService,
        IUpdateService updateService,
        IStorageService storageService,
        IBatteryEstimationService estimationService,
        BatteryAlertSoundService? batteryAlertSoundService = null,
        MouseDpiMonitorService? dpiMonitorService = null)
    {
        _monitorService = monitorService;
        _settingsService = settingsService;
        _hidService = hidService;
        _autoStartService = autoStartService;
        _updateService = updateService;
        _storageService = storageService;
        _estimationService = estimationService;
        _batteryAlertSoundService = batteryAlertSoundService;
        _dpiMonitorService = dpiMonitorService;
        if (_dpiMonitorService != null)
            _dpiMonitorService.DpiChanged += OnDpiChanged;

        _monitorService.BatteryStateChanged += OnBatteryStateChanged;
        _monitorService.EstimateChanged += OnEstimateChanged;

        LoadSettings();
        UpdateEstimateLabels();
    }

    private void OnDpiChanged(DpiReading? reading) =>
        Dispatcher.UIThread.Post(() => ApplyDpiReport(reading));

    internal void ApplyDpiReport(DpiReading? reading)
    {
        _lastDpi = reading;
        if (reading is { } dpi)
        {
            CurrentDpiText = dpi.X == dpi.Y ? $"{dpi.X} DPI" : $"{dpi.X}×{dpi.Y} DPI";
            // The receiver does not report the colour customised in CORE.
            DpiStageBrush = ResolveBrush(dpi.Stage is >= 0 and <= 3
                ? $"DpiStage{dpi.Stage + 1}Brush"
                : "DpiStageUnknownBrush", "#929AA5");
        }
        else
        {
            CurrentDpiText = string.Empty;
        }
        HasDpi = reading.HasValue;
        UpdateDpiTooltip();
    }

    private void UpdateDpiTooltip()
    {
        if (_lastDpi is not { } dpi)
        {
            DpiTooltip = string.Empty;
            return;
        }

        DpiTooltip = string.Format(Localization.Text("DpiTooltipFactory"), dpi.Stage + 1, dpi.X, dpi.Y,
            Localization.Text(dpi.Stage switch
            {
                0 => "DpiColorYellow",
                1 => "DpiColorBlue",
                2 => "DpiColorOrange",
                3 => "DpiColorGreen",
                _ => "DpiColorUnknown"
            }));
    }

    private static IBrush ResolveBrush(string key, string fallbackHex) =>
        Application.Current is { } app &&
        app.TryGetResource(key, app.ActualThemeVariant, out var value) && value is IBrush brush
            ? brush
            : new SolidColorBrush(Color.Parse(fallbackHex));

    /// <summary>
    /// DPI chip: only after the mouse reported DPI (Model O Pro receivers), only while it is
    /// connected, and only when the user keeps "Show DPI" on. Every other mouse hides it.
    /// </summary>
    public bool ShowDpiChip => HasDpi && ShowDpi && IsDpiReceiver && ConnectionState == ConnectionState.Connected;

    /// <summary>Widget text when the mouse can't report a level: "— Mouse asleep" / "— Disconnected".</summary>
    public string WidgetOfflineText => ConnectionState switch
    {
        ConnectionState.Sleeping => "— " + Localization.Text("MouseAsleep"),
        ConnectionState.NotConnected => "— " + Localization.Text("Disconnected"),
        ConnectionState.Connecting => "— " + Localization.Text("Connecting"),
        _ => string.Empty
    };

    public bool IsWidgetOffline => WidgetOfflineText.Length > 0;

    /// <summary>Widget turns its text and border red below 20% while discharging.</summary>
    public bool IsLowBattery => IsConnected && !IsCharging && BatteryLevel < 20;

    public string CriticalSoundDescription =>
        string.Format(Localization.Text("CriticalSoundDescription"), CriticalThreshold);

    /// <summary>Widget context menu "Hide widget": persist the choice right away.</summary>
    public void HideBatteryWidget()
    {
        ShowBatteryWidget = false;
        var settings = _settingsService.Current.Clone();
        settings.ShowBatteryWidget = false;
        _settingsService.Save(settings);
    }

    private void UpdateEstimateLabels()
    {
        EstimateLabel = Localization.Text(IsCharging ? "TimeToFullShort" : "TimeRemainingShort");
        EstimateTooltip = Localization.Text(IsCharging ? "TimeToFull" : "TimeRemaining");
    }

    public bool IsSystemThemeSelected => CurrentTheme == "system";
    public bool IsDarkThemeSelected => CurrentTheme == "dark";
    public bool IsLightThemeSelected => CurrentTheme == "light";
    public int ThemeSelectedIndex => CurrentTheme switch
    {
        "dark" => 1,
        "light" => 2,
        _ => 0
    };
    public object? SettingsOverlayContent => IsSettingsOpen ? this : null;
    public bool HasCriticalBatterySound => !string.IsNullOrWhiteSpace(CriticalBatterySoundPath);
    public string CriticalBatterySoundName => HasCriticalBatterySound
        ? Path.GetFileName(CriticalBatterySoundPath)
        : Localization.Text("NoSoundSelected");

    public async Task InitializeAsync()
    {
        await _monitorService.StartAsync();
        StartLastSyncTimer();

        if (_updateService.IsUpdatePendingRestart())
        {
            ConfigurePendingUpdateState();
            return;
        }

        if (Program.SkipUpdateCheckOnLaunch)
        {
            UpdateStatusText = $"Updated to v{_updateService.CurrentVersion}";
            UpdateUpdateIndicator();
            return;
        }

        _ = DelayedStartupUpdateCheckAsync();
    }

    private async Task DelayedStartupUpdateCheckAsync()
    {
        try
        {
            await Task.Delay(TimeSpan.FromSeconds(3));
            await CheckForUpdateAsync();
        }
        catch
        {
            // Best-effort background update check.
        }
    }

    private void ConfigurePendingUpdateState()
    {
        IsUpdateAvailable = true;
        IsUpdateReadyToInstall = true;
        IsDownloadingUpdate = false;
        UpdateDownloadProgress = 100;
        UpdateActionButtonText = "Restart to Update";
        UpdateStatusText = "Update ready — restart to apply";
        UpdateUpdateIndicator();
    }

    private void UpdateUpdateIndicator()
    {
        if (IsUpdateReadyToInstall)
        {
            ShowUpdateIndicator = true;
            UpdateIndicatorText = "Update ready";
            UpdateIndicatorActionText = "Restart";
            ShowUpdateIndicatorAction = true;
            return;
        }

        if (IsDownloadingUpdate)
        {
            ShowUpdateIndicator = true;
            UpdateIndicatorText = $"Updating {UpdateDownloadProgress}%";
            ShowUpdateIndicatorAction = false;
            return;
        }

        if (IsUpdateAvailable)
        {
            ShowUpdateIndicator = true;
            UpdateIndicatorText = string.IsNullOrWhiteSpace(UpdateVersion)
                ? "Update available"
                : $"v{UpdateVersion} available";
            UpdateIndicatorActionText = "Download";
            ShowUpdateIndicatorAction = true;
            return;
        }

        ShowUpdateIndicator = false;
        ShowUpdateIndicatorAction = false;
        UpdateIndicatorText = string.Empty;
        UpdateIndicatorActionText = "Open";
    }

    [RelayCommand]
    private async Task UpdateIndicatorActionAsync()
    {
        if (IsCheckingUpdate || IsDownloadingUpdate)
            return;

        if (IsUpdateAvailable || IsUpdateReadyToInstall)
        {
            await DownloadUpdateAsync();
            return;
        }

        OpenSettings();
    }

    private void OnBatteryStateChanged(BatteryState state)
    {
        ProcessCriticalBatterySound(state);

        Dispatcher.UIThread.Post(() =>
        {
            BatteryLevel = state.Level;
            IsCharging = state.IsCharging;
            IsConnected = state.Connection == ConnectionState.Connected ||
                         state.Connection == ConnectionState.LastKnown ||
                         state.Connection == ConnectionState.Sleeping;
            BatteryPercentText = IsConnected ? $"{state.Level}%" : "—";
            IsDpiReceiver = state.VendorId == MouseDpiMonitorService.VendorId &&
                            state.ProductId == MouseDpiMonitorService.ReceiverProductId;
            DeviceName = state.DeviceName;
            ConnectionState = state.Connection;
            UpdateEstimateLabels();

            if (state.LastReadTime > DateTime.MinValue)
                _lastReadTime = state.LastReadTime;

            if (state.LastChargeTime.HasValue)
                LastChargedText = state.LastChargeTime.Value.ToString("MMM d, h:mm tt");
            else
                LastChargedText = "—";

            if (state.LastChargeLevel.HasValue)
                ChargedToText = $"{state.LastChargeLevel.Value}%";
            else
                ChargedToText = "—";

            UpdateLastSyncTextAndSchedule();
        });
    }

    private void ProcessCriticalBatterySound(BatteryState state)
    {
        string? soundPath = null;

        lock (_criticalSoundLock)
        {
            bool isBelowThreshold = state.Connection == ConnectionState.Connected &&
                                    !state.IsCharging &&
                                    state.Level <= _settingsService.Current.CriticalBatteryThreshold;

            if (!isBelowThreshold)
            {
                _criticalSoundFired = false;
                _criticalSoundDeviceName = state.DeviceName;
                return;
            }

            if (!string.Equals(_criticalSoundDeviceName, state.DeviceName, StringComparison.Ordinal))
            {
                _criticalSoundDeviceName = state.DeviceName;
                _criticalSoundFired = false;
            }

            if (_criticalSoundFired)
                return;

            _criticalSoundFired = true;
            soundPath = _settingsService.Current.CriticalBatterySoundPath;
        }

        if (!string.IsNullOrWhiteSpace(soundPath) && _batteryAlertSoundService != null)
            _ = _batteryAlertSoundService.PlayAsync(soundPath);
    }

    partial void OnCriticalBatterySoundPathChanged(string value)
    {
        OnPropertyChanged(nameof(HasCriticalBatterySound));
        OnPropertyChanged(nameof(CriticalBatterySoundName));

        lock (_criticalSoundLock)
            _criticalSoundFired = false;
    }

    private void OnEstimateChanged(BatteryEstimate estimate)
    {
        Dispatcher.UIThread.Post(() =>
        {
            HasEstimate = estimate.IsValid;
            if (estimate.IsValid)
            {
                TimeRemaining = estimate.TimeRemaining;
                TimeRemainingText = FormatDuration(estimate.TimeRemaining);
            }
            else
            {
                TimeRemainingText = "—";
            }
            UpdateFullChargeDuration();
        });
    }

    private void UpdateFullChargeDuration()
    {
        // Get display name from current state
        string displayName = _monitorService.CurrentState.DeviceName;
        DateTime now = DateTime.UtcNow;

        if (string.IsNullOrEmpty(displayName) || displayName == "Glorious Mouse")
        {
            FullChargeDurationText = "—";
            _lastFullChargeDurationDeviceName = string.Empty;
            _lastFullChargeDurationRefreshUtc = DateTime.MinValue;
            return;
        }

        bool sameDevice = string.Equals(
            _lastFullChargeDurationDeviceName,
            displayName,
            StringComparison.Ordinal);

        if (sameDevice &&
            _lastFullChargeDurationRefreshUtc != DateTime.MinValue &&
            now - _lastFullChargeDurationRefreshUtc < FullChargeDurationRefreshInterval)
        {
            return;
        }

        _lastFullChargeDurationDeviceName = displayName;
        _lastFullChargeDurationRefreshUtc = now;

        // Load cached charge data and iterate to find the current device's learned rates
        // The estimation service only has in-memory state for the currently active device,
        // so we iterate through all keys and find the one with non-null, positive discharge rate
        var chargeData = _storageService.LoadChargeData();
        foreach (var kvp in chargeData.Devices)
        {
            var rates = _estimationService.GetLearnedRates(kvp.Key);
            if (rates?.DischargeRate is not double rate || rate <= 0)
                continue;

            double totalHours = 100.0 / rate;

            // Keep display bounded but allow realistic multi-day durations.
            if (totalHours > FullChargeDurationDisplayCapHours)
            {
                FullChargeDurationText = Localization.Text("Over30Days");
                return;
            }

            var span = TimeSpan.FromHours(totalHours);
            FullChargeDurationText = "~" + FormatDuration(span, includeMinutesForDays: false);
            return;
        }

        // No device with learned rates found
        FullChargeDurationText = "—";
    }

    private string FormatDuration(TimeSpan span, bool includeMinutesForDays = false)
    {
        // Short units in both languages so values fit the 22 px stat cards ("1d 13h" / "1 д 13 ч").
        var (d, h, m) = Localization.CurrentLanguage == "ru" ? (" д", " ч", " мин") : ("d", "h", "m");

        if (span.TotalHours >= 24)
        {
            int days = (int)span.TotalDays;
            int hours = span.Hours;
            int mins = span.Minutes;

            if (includeMinutesForDays && mins > 0)
                return $"{days}{d} {hours}{h} {mins}{m}";

            return hours > 0 ? $"{days}{d} {hours}{h}" : $"{days}{d}";
        }

        int shortHours = (int)span.TotalHours;
        int shortMins = span.Minutes;
        return shortHours > 0 ? $"{shortHours}{h} {shortMins}{m}" : $"{shortMins}{m}";
    }

    private static string RussianUnit(int value, string one, string few, string many)
    {
        var n = Math.Abs(value) % 100;
        if (n is >= 11 and <= 14) return many;
        return (Math.Abs(value) % 10) switch { 1 => one, >= 2 and <= 4 => few, _ => many };
    }

    private void LoadSettings()
    {
        TryEnableBetaByDefaultForPrereleaseBuild();

        var s = _settingsService.Current;
        StartWithOS = s.StartWithOS;
        StartMinimized = s.StartMinimized;
        NotificationsEnabled = s.NotificationsEnabled;
        CriticalBatterySoundPath = s.CriticalBatterySoundPath;
        ShowPercentageOnTray = s.ShowPercentageOnTrayIcon;
        ShowBatteryWidget = s.ShowBatteryWidget;
        ShowDpi = s.ShowDpi;
        Language = Localization.Normalize(s.Language);
        LanguageSelectedIndex = Language switch { "ru" => 2, "en" => 1, _ => 0 };
        RefreshInterval = s.RefreshIntervalSeconds;
        LowThreshold = s.LowBatteryThreshold;
        CriticalThreshold = s.CriticalBatteryThreshold;
        NotificationCooldownMinutes = s.NotificationCooldownMinutes;
        DebugLogging = s.DebugLogging;
        EnableBetaUpdates = s.EnableBetaUpdates;
        CurrentTheme = NormalizeTheme(s.Theme);
        IsDarkTheme = CurrentTheme == "dark";
    }

    private void TryEnableBetaByDefaultForPrereleaseBuild()
    {
        if (_betaDefaultAppliedThisSession)
            return;

        _betaDefaultAppliedThisSession = true;

        var current = _settingsService.Current;
        if (current.EnableBetaUpdates)
            return;

        if (current.HasSetBetaChannelPreference)
            return;

        // Prerelease builds (e.g. 3.3.0-beta.13) should default to beta channel.
        if (!IsPrereleaseVersion(_updateService.CurrentVersion))
            return;

        var migrated = current.Clone();
        migrated.EnableBetaUpdates = true;
        _settingsService.Save(migrated);
    }

    private static bool IsPrereleaseVersion(string version)
    {
        return !string.IsNullOrWhiteSpace(version) &&
               version.Contains('-', StringComparison.Ordinal);
    }

    [RelayCommand]
    private void OpenSettings()
    {
        LoadSettings();
        IsSettingsOpen = true;
    }

    [RelayCommand]
    private void CloseSettings()
    {
        LoadSettings();
        PreviewTheme(CurrentTheme);
        IsSettingsOpen = false;
    }

    [RelayCommand]
    private void SaveSettings()
    {
        bool oldBetaSetting = _settingsService.Current.EnableBetaUpdates;

        var settings = _settingsService.Current.Clone();
        settings.StartWithOS = StartWithOS;
        settings.StartMinimized = StartMinimized;
        settings.NotificationsEnabled = NotificationsEnabled;
        settings.CriticalBatterySoundPath = CriticalBatterySoundPath;
        settings.ShowPercentageOnTrayIcon = ShowPercentageOnTray;
        settings.ShowBatteryWidget = ShowBatteryWidget;
        settings.ShowDpi = ShowDpi;
        settings.Language = Language;
        settings.RefreshIntervalSeconds = Math.Clamp(RefreshInterval, 1, 60);
        settings.LowBatteryThreshold = Math.Clamp(LowThreshold, 5, 50);
        settings.CriticalBatteryThreshold = Math.Clamp(CriticalThreshold, 5, 30);
        settings.NotificationCooldownMinutes = Math.Clamp(NotificationCooldownMinutes, 1, 60);
        settings.DebugLogging = DebugLogging;
        settings.Theme = CurrentTheme;
        settings.EnableBetaUpdates = EnableBetaUpdates;
        settings.HasSetBetaChannelPreference = true;

        bool betaSettingChanged = oldBetaSetting != settings.EnableBetaUpdates;

        _settingsService.Save(settings);
        _autoStartService.SetAutoStart(settings.StartWithOS);
        IsSettingsOpen = false;
        ShowToast(Localization.Text("ToastSaved"));

        if (betaSettingChanged)
        {
            IsUpdateAvailable = false;
            IsUpdateReadyToInstall = false;
            IsDownloadingUpdate = false;
            UpdateDownloadProgress = 0;
            UpdateActionButtonText = "Download Update";
            UpdateStatusText = settings.EnableBetaUpdates
                ? "Beta channel enabled — checking for beta updates..."
                : "Stable channel enabled — checking for stable updates...";
            UpdateUpdateIndicator();

            _ = CheckForUpdateAsync();
        }
    }

    [RelayCommand]
    private async Task TestCriticalBatterySoundAsync()
    {
        if (string.IsNullOrWhiteSpace(CriticalBatterySoundPath) || _batteryAlertSoundService == null)
            return;

        if (!await _batteryAlertSoundService.PlayAsync(CriticalBatterySoundPath))
            ShowToast(Localization.Text("ToastSoundFailed"));
    }

    [RelayCommand]
    private void ToggleTheme()
    {
        SetTheme(IsDarkTheme ? "light" : "dark");
    }

    [RelayCommand]
    private void SetTheme(string? theme)
    {
        CurrentTheme = NormalizeTheme(theme);
        PreviewTheme(CurrentTheme);
    }

    [RelayCommand]
    private void Rescan()
    {
        _monitorService.TriggerRescan();
    }

    [RelayCommand]
    private void ScanHidDevices()
    {
        DevOutput = _hidService.GetHidDiagnostics();
        ShowToast(Localization.Text("ToastScanComplete"));
    }

    [RelayCommand]
    private void CaptureReport()
    {
        // Try to capture from current device
        var profiles = _storageService.LoadProfiles();
        if (profiles.Count > 0)
        {
            var data = _hidService.CaptureRawReport(profiles[0]);
            if (data != null)
            {
                DevOutput = $"Raw report ({data.Length} bytes):\n{BitConverter.ToString(data)}";
                return;
            }
        }
        DevOutput = "No device connected to capture report from.";
    }

    [RelayCommand]
    private async Task CopyOutputAsync()
    {
        if (Avalonia.Application.Current?.ApplicationLifetime is
            Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime desktop)
        {
            var clipboard = desktop.MainWindow?.Clipboard;
            if (clipboard != null)
            {
                var item = new Avalonia.Input.DataTransferItem();
                item.Set(Avalonia.Input.DataFormat.Text, DevOutput);
                var data = new Avalonia.Input.DataTransfer();
                data.Add(item);
                await clipboard.SetDataAsync(data);
                ShowToast(Localization.Text("ToastCopied"));
            }
        }
    }

    [RelayCommand]
    private void CaptureDiagnostics()
    {
        var diag = $"=== Glorious Battery Monitor Diagnostics ===\n" +
                   $"Time: {DateTime.Now:O}\n" +
                   $"Version: {_updateService.CurrentVersion}\n\n" +
                   $"=== Battery State ===\n" +
                   $"Level: {BatteryLevel}%\n" +
                   $"Charging: {IsCharging}\n" +
                   $"Connection: {ConnectionState}\n" +
                   $"Device: {DeviceName}\n\n" +
                   $"=== HID Devices ===\n" +
                   _hidService.GetHidDiagnostics();
        DevOutput = diag;
        ShowToast(Localization.Text("ToastDiagnostics"));
    }

    [RelayCommand]
    private async Task CheckForUpdateAsync()
    {
        IsCheckingUpdate = true;
        UpdateUpdateIndicator();
        try
        {
            if (_updateService.IsUpdatePendingRestart())
            {
                ConfigurePendingUpdateState();
                return;
            }

            IsUpdateAvailable = false;
            IsUpdateReadyToInstall = false;
            UpdateDownloadProgress = 0;
            UpdateActionButtonText = "Download Update";
            UpdateStatusText = EnableBetaUpdates
                ? "Checking beta updates..."
                : "Checking stable updates...";

            var result = await _updateService.CheckForUpdateAsync();
            if (result == null)
            {
                string channelName = EnableBetaUpdates ? "beta" : "stable";
                UpdateStatusText =
                    $"Up to date ({channelName}) — v{_updateService.CurrentVersion}";
            }
            else
            {
                string channelName = EnableBetaUpdates ? "Beta" : "Stable";
                UpdateStatusText = $"{channelName} update available: v{result.NewVersion}";
                UpdateVersion = result.NewVersion;
                IsUpdateAvailable = true;
                UpdateUpdateIndicator();
            }
        }
        catch
        {
            UpdateStatusText = "Update check failed";
            UpdateUpdateIndicator();
        }
        finally
        {
            IsCheckingUpdate = false;
            UpdateUpdateIndicator();
        }
    }

    [RelayCommand]
    private async Task DownloadUpdateAsync()
    {
        if (IsUpdateReadyToInstall || _updateService.IsUpdatePendingRestart())
        {
            UpdateStatusText = "Restarting to apply update...";
            UpdateUpdateIndicator();
            bool started = _updateService.ApplyPendingUpdateAndRestart(new[] { Program.SkipUpdateCheckArg });
            if (started)
            {
                if (Avalonia.Application.Current?.ApplicationLifetime
                    is Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime desktop)
                {
                    desktop.Shutdown();
                }
                return;
            }

            UpdateStatusText = "Could not start updater — try again";
            UpdateUpdateIndicator();
            return;
        }

        IsDownloadingUpdate = true;
        UpdateDownloadProgress = 0;
        UpdateUpdateIndicator();
        var progress = new Progress<int>(p =>
        {
            UpdateDownloadProgress = p;
            UpdateStatusText = $"Downloading update... {p}%";
            UpdateUpdateIndicator();
        });

        var success = await _updateService.DownloadUpdateAsync(progress);
        IsDownloadingUpdate = false;

        if (success)
        {
            ConfigurePendingUpdateState();
            ShowToast(Localization.Text("ToastUpdateReady"));
        }
        else
        {
            UpdateStatusText = "Download failed — try again";
            UpdateUpdateIndicator();
        }

        UpdateUpdateIndicator();
    }

    private bool _disposed;
    private DispatcherTimer? _toastTimer;
    private DispatcherTimer? _lastSyncTimer;

    partial void OnCurrentThemeChanged(string value)
    {
        IsDarkTheme = value == "dark";
        OnPropertyChanged(nameof(IsSystemThemeSelected));
        OnPropertyChanged(nameof(IsDarkThemeSelected));
        OnPropertyChanged(nameof(IsLightThemeSelected));
        OnPropertyChanged(nameof(ThemeSelectedIndex));
    }

    partial void OnLanguageChanged(string value)
    {
        Localization.Apply(value);
        LanguageSelectedIndex = value switch { "ru" => 2, "en" => 1, _ => 0 };
        UpdateEstimateLabels();
        UpdateLastSyncText();
        UpdateDpiTooltip();
        OnPropertyChanged(nameof(CriticalBatterySoundName));
        OnPropertyChanged(nameof(CriticalSoundDescription));
        OnPropertyChanged(nameof(WidgetOfflineText));

        // Durations are formatted per language; re-render them now instead of on the next poll.
        if (HasEstimate)
            TimeRemainingText = FormatDuration(TimeRemaining);
        _lastFullChargeDurationRefreshUtc = DateTime.MinValue;
        UpdateFullChargeDuration();
    }

    partial void OnLanguageSelectedIndexChanged(int value)
    {
        Language = value switch { 2 => "ru", 1 => "en", _ => "system" };
    }

    partial void OnIsSettingsOpenChanged(bool value)
    {
        OnPropertyChanged(nameof(SettingsOverlayContent));
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        if (_dpiMonitorService != null)
            _dpiMonitorService.DpiChanged -= OnDpiChanged;
        _monitorService.BatteryStateChanged -= OnBatteryStateChanged;
        _monitorService.EstimateChanged -= OnEstimateChanged;
        _lastSyncTimer?.Stop();
        _lastSyncTimer = null;
        _toastTimer?.Stop();
        _toastTimer = null;
    }

    private void StartLastSyncTimer()
    {
        _lastSyncTimer ??= new DispatcherTimer();
        _lastSyncTimer.Stop();
        _lastSyncTimer.Tick -= OnLastSyncTimerTick;
        _lastSyncTimer.Tick += OnLastSyncTimerTick;
        UpdateLastSyncTextAndSchedule();
    }

    private void OnLastSyncTimerTick(object? sender, EventArgs e)
    {
        _lastSyncTimer?.Stop();
        UpdateLastSyncTextAndSchedule();
    }

    private void UpdateLastSyncTextAndSchedule()
    {
        UpdateLastSyncText();
        ScheduleNextLastSyncUpdate();
    }

    private void ScheduleNextLastSyncUpdate()
    {
        if (_lastSyncTimer == null)
            return;

        if (_lastReadTime == null)
        {
            _lastSyncTimer.Stop();
            return;
        }

        _lastSyncTimer.Interval = GetNextLastSyncUpdateDelay(_lastReadTime.Value, DateTime.UtcNow);
        _lastSyncTimer.Start();
    }

    private void UpdateLastSyncText()
    {
        if (_lastReadTime == null)
        {
            LastSyncText = "—";
            return;
        }

        var elapsed = DateTime.UtcNow - _lastReadTime.Value;

        if (elapsed.TotalSeconds < 5)
        {
            LastSyncText = Localization.Text("JustNow");
            return;
        }

        if (Localization.CurrentLanguage == "ru")
        {
            var amount = elapsed.TotalMinutes < 1 ? (int)elapsed.TotalSeconds
                : elapsed.TotalHours < 1 ? (int)elapsed.TotalMinutes
                : elapsed.TotalDays < 1 ? (int)elapsed.TotalHours : (int)elapsed.TotalDays;
            var unit = elapsed.TotalMinutes < 1 ? RussianUnit(amount, "секунда", "секунды", "секунд")
                : elapsed.TotalHours < 1 ? RussianUnit(amount, "минута", "минуты", "минут")
                : elapsed.TotalDays < 1 ? RussianUnit(amount, "час", "часа", "часов")
                : RussianUnit(amount, "день", "дня", "дней");
            LastSyncText = $"{amount} {unit} назад";
        }
        else
        {
            LastSyncText = elapsed.TotalSeconds < 60 ? $"{(int)elapsed.TotalSeconds}s ago"
                : elapsed.TotalMinutes < 60 ? $"{(int)elapsed.TotalMinutes}m ago"
                : elapsed.TotalHours < 24 ? $"{(int)elapsed.TotalHours}h ago"
                : $"{(int)elapsed.TotalDays}d ago";
        }
    }

    private static TimeSpan GetNextLastSyncUpdateDelay(DateTime lastReadTimeUtc, DateTime nowUtc)
    {
        var elapsed = nowUtc - lastReadTimeUtc;
        if (elapsed < TimeSpan.Zero)
            return TimeSpan.FromSeconds(1);

        TimeSpan delay = elapsed.TotalSeconds < 5
            ? TimeSpan.FromSeconds(5) - elapsed
            : elapsed.TotalSeconds < 60
                ? TimeSpan.FromSeconds(Math.Floor(elapsed.TotalSeconds) + 1) - elapsed
                : elapsed.TotalMinutes < 60
                    ? TimeSpan.FromMinutes(Math.Floor(elapsed.TotalMinutes) + 1) - elapsed
                    : elapsed.TotalHours < 24
                        ? TimeSpan.FromHours(Math.Floor(elapsed.TotalHours) + 1) - elapsed
                        : TimeSpan.FromDays(Math.Floor(elapsed.TotalDays) + 1) - elapsed;

        return delay <= TimeSpan.Zero ? MinimumLastSyncUpdateInterval : delay;
    }

    public void ShowToast(string message)
    {
        ToastMessage = message;
        IsToastVisible = true;

        var toastTimer = _toastTimer ??= new DispatcherTimer
        {
            Interval = ToastDuration
        };

        toastTimer.Stop();
        toastTimer.Tick -= OnToastTimerTick;
        toastTimer.Tick += OnToastTimerTick;
        toastTimer.Start();
    }

    private void OnToastTimerTick(object? sender, EventArgs e)
    {
        if (_toastTimer == null)
            return;

        _toastTimer.Stop();
        IsToastVisible = false;
    }

    private void PreviewTheme(string theme)
    {
        if (Application.Current is App app)
            app.PreviewTheme(theme);
    }

    private static string NormalizeTheme(string? theme)
    {
        var normalized = string.IsNullOrWhiteSpace(theme)
            ? "system"
            : theme.Trim().ToLowerInvariant();

        return normalized is "dark" or "light" ? normalized : "system";
    }
}
