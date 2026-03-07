using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class BatteryMonitorService : IBatteryMonitorService, IDisposable
{
    private readonly ILogger<BatteryMonitorService> _logger;
    private readonly IHidDeviceService _hidDeviceService;
    private readonly ISettingsService _settingsService;
    private readonly IStorageService _storageService;
    private readonly IBatteryEstimationService _estimationService;
    private readonly INotificationService _notificationService;

    private readonly object _stateLock = new();
    private CancellationTokenSource? _cts;
    private Task? _pollingTask;
    private DeviceProfile? _activeProfile;
    private int _consecutiveFailures;
    private DateTime _lastReconnectAttempt = DateTime.MinValue;
    private bool _disposed;
    private bool _rescanRequested;

    // Probe exhaustion: when known devices are present but all probe candidates fail,
    // avoid re-entering "Connecting" state every poll cycle (which takes 15-20s per attempt
    // and makes the UI appear stuck on "connecting"). Instead, stay NotConnected and
    // retry the full probe chain less frequently.
    private bool _probeExhausted;

    // Last valid (non-zero) battery level from a successful read.
    // Used to suppress transient 0% readings when the mouse is sleeping (Status 0xA4).
    private int _lastPositiveLevel;

    // Sleep detection: count consecutive successful reads that return Level=0%.
    // After a threshold, transition to Sleeping instead of staying Connected.
    private int _consecutiveZeroReads;
    private const int ConsecutiveZeroReadsForSleep = 3;

    // Wired-device-based charging: the wireless dongle cannot reliably report
    // charging state (mouse sleeps on RF when USB cable is plugged in).
    // Instead, we detect the wired HID device appearing as the charging signal.
    // We estimate charging progress using a Li-ion charge curve model rather than
    // reading the wired device (its voltage-based reading is inflated by charge current).
    private bool _lastWiredPresent;
    private DateTime? _chargeStartTime;
    private int _chargeStartLevel;

    // Post-disconnect settling: when the USB cable is unplugged, the device stops
    // responding to GetFeature for a few seconds while it transitions back to wireless.
    // Skip polling during this window to avoid accumulating false failures.
    private DateTime? _cableDisconnectTime;
    private static readonly TimeSpan PostDisconnectSettlingDelay = TimeSpan.FromSeconds(5);

    // Li-ion charge curve constants (typical for 500-700mAh gaming mouse cells)
    private const double CcRatePerHour = 60.0;   // CC phase (<80%): 60%/hr
    private const double CvRateMaxPerHour = 60.0; // CV phase start rate at 80%
    private const double CvRateMinPerHour = 10.0; // CV phase end rate at 100%
    private const int CvThreshold = 80;           // CC→CV transition point
    private const int MaxEstimatedLevel = 99;     // Can't confirm 100% without real reading

    private static readonly TimeSpan ReconnectDebounce = TimeSpan.FromMilliseconds(900);
    private static readonly TimeSpan ExhaustedProbeDebounce = TimeSpan.FromSeconds(60);
    private const int MaxConsecutiveFailuresBeforeReconnect = 3;

    // CandidateF does not always get a response on every poll cycle — the device
    // answers intermittently across RIDs, and each full miss (all 4 RIDs timeout)
    // takes ~12 seconds. A threshold of 3 would trigger reconnect within ~36s even
    // when the device responds every other cycle. Use a higher threshold so transient
    // misses don't cause needless re-probing.
    private const int MaxConsecutiveFailuresCandidateF = 6;

    public BatteryState CurrentState { get; private set; } = BatteryState.Disconnected;
    public BatteryEstimate CurrentEstimate { get; private set; } = BatteryEstimate.Invalid;
    public bool IsRunning => _pollingTask != null && !_pollingTask.IsCompleted;

    public event Action<BatteryState>? BatteryStateChanged;
    public event Action<BatteryEstimate>? EstimateChanged;

    public BatteryMonitorService(
        ILogger<BatteryMonitorService> logger,
        IHidDeviceService hidDeviceService,
        ISettingsService settingsService,
        IStorageService storageService,
        IBatteryEstimationService estimationService,
        INotificationService notificationService)
    {
        _logger = logger;
        _hidDeviceService = hidDeviceService;
        _settingsService = settingsService;
        _storageService = storageService;
        _estimationService = estimationService;
        _notificationService = notificationService;
    }

    public Task StartAsync(CancellationToken cancellationToken = default)
    {
        if (IsRunning)
        {
            _logger.LogWarning("BatteryMonitorService is already running");
            return Task.CompletedTask;
        }

        // Check for no-HID development mode
        string? noHid = Environment.GetEnvironmentVariable("GBM_NO_HID");
        if (!string.IsNullOrEmpty(noHid) &&
            (noHid.Equals("1", StringComparison.Ordinal) ||
             noHid.Equals("true", StringComparison.OrdinalIgnoreCase)))
        {
            _logger.LogWarning("GBM_NO_HID is set. Running in UI development mode (no HID operations).");
            UpdateState(new BatteryState
            {
                Level = 75,
                IsCharging = false,
                Connection = ConnectionState.Connected,
                Health = BatteryHealth.Good,
                DeviceName = "Mock Device (GBM_NO_HID)",
                LastReadTime = DateTime.UtcNow
            });
            return Task.CompletedTask;
        }

        _cts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        _pollingTask = Task.Run(() => PollingLoop(_cts.Token), _cts.Token);

        _logger.LogInformation("BatteryMonitorService started");
        return Task.CompletedTask;
    }

    public async Task StopAsync()
    {
        if (_cts == null)
            return;

        _logger.LogInformation("Stopping BatteryMonitorService...");

        try
        {
            _cts.Cancel();

            if (_pollingTask != null)
            {
                await _pollingTask.ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException)
        {
            // Expected
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error stopping BatteryMonitorService");
        }
        finally
        {
            _cts.Dispose();
            _cts = null;
            _pollingTask = null;
        }

        _logger.LogInformation("BatteryMonitorService stopped");
    }

    public void TriggerRescan()
    {
        _logger.LogInformation("Rescan triggered");
        _rescanRequested = true;
        _activeProfile = null;
        _consecutiveFailures = 0;
        _consecutiveZeroReads = 0;
        _lastPositiveLevel = 0;
        _lastWiredPresent = false;
        _chargeStartTime = null;
        _chargeStartLevel = 0;
        _cableDisconnectTime = null;
        _probeExhausted = false;
    }

    private async Task PollingLoop(CancellationToken cancellationToken)
    {
        // Try to restore cached profile first
        TryRestoreCachedProfile();

        // Seed estimation service with historical rates from disk
        SeedHistoricalRates();

        // Check if USB cable is already plugged in at startup (PID 0x824A present).
        // Without this, the wired-presence transition only fires on hot-plug events,
        // so restarting the app while charging would miss the cable.
        SeedWiredPresenceOnStartup();

        // Poll immediately on startup — don't wait for the first timer tick
        await PollOnceAsync(cancellationToken).ConfigureAwait(false);

        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                int intervalSeconds = _settingsService.Current.RefreshIntervalSeconds;
                if (intervalSeconds < 1)
                    intervalSeconds = 5;

                using var timer = new PeriodicTimer(TimeSpan.FromSeconds(intervalSeconds));

                while (await timer.WaitForNextTickAsync(cancellationToken).ConfigureAwait(false))
                {
                    await PollOnceAsync(cancellationToken).ConfigureAwait(false);
                }
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "[MONITOR] Error in polling loop. Restarting after delay...");

                try
                {
                    await Task.Delay(TimeSpan.FromSeconds(5), cancellationToken).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
            }
        }
    }

    private Task PollOnceAsync(CancellationToken cancellationToken)
    {
        try
        {
            if (_rescanRequested)
            {
                _rescanRequested = false;
                _activeProfile = null;
                _consecutiveFailures = 0;
                _estimationService.Reset(CurrentState.DeviceName);
            }

            // If no active device, try to find one
            if (_activeProfile == null)
            {
                AttemptDeviceDiscovery();

                if (_activeProfile == null)
                {
                    if (CurrentState.Connection != ConnectionState.NotConnected &&
                        CurrentState.Connection != ConnectionState.LastKnown)
                    {
                        TransitionToDisconnected();
                    }

                    return Task.CompletedTask;
                }
            }

            // Check if the wired USB cable is plugged in (charging signal).
            string modelName = _activeProfile.ModelName;
            bool wiredPresent = _hidDeviceService.IsWiredDevicePresent(modelName);

            if (wiredPresent != _lastWiredPresent)
            {
                _logger.LogInformation(
                    "[MONITOR] Wired device presence changed: {Old} → {New} for {Model}",
                    _lastWiredPresent, wiredPresent, modelName);

                if (wiredPresent && !_lastWiredPresent)
                {
                    // Cable just plugged in — record charge start for estimation.
                    // Do NOT reset _activeProfile or trigger reconnect: the CandidateF
                    // poll on PID=0x824D remains valid while the cable is in.
                    _chargeStartTime = DateTime.UtcNow;
                    _chargeStartLevel = _lastPositiveLevel > 0 ? _lastPositiveLevel : 0;
                    _consecutiveFailures = 0;
                    _logger.LogInformation(
                        "[MONITOR] Charge started at {Level}% for {Model}",
                        _chargeStartLevel, modelName);
                }
                else if (!wiredPresent)
                {
                    // Cable unplugged — clear charge tracking.
                    // The device needs a few seconds to settle back into wireless mode
                    // before it will respond to GetFeature again. Record the disconnect
                    // time so PollOnceAsync can skip reads during the settling period.
                    _chargeStartTime = null;
                    _chargeStartLevel = 0;
                    _consecutiveFailures = 0;
                    _cableDisconnectTime = DateTime.UtcNow;
                    _logger.LogInformation(
                        "[MONITOR] USB cable disconnected for {Model} — resuming wireless polling after {Delay}s settling delay",
                        modelName, PostDisconnectSettlingDelay.TotalSeconds);
                }

                _lastWiredPresent = wiredPresent;
            }

            if (wiredPresent)
            {
                // Clear settling delay if cable is plugged back in.
                _cableDisconnectTime = null;

                // Charging: estimate battery level from Li-ion charge curve model.
                // The wireless dongle returns 0% (mouse sleeping on RF) and the wired
                // device's voltage-based reading is inflated, so we model it instead.
                int estimatedLevel = EstimateChargingLevel();
                _consecutiveFailures = 0;
                ProcessSuccessfulRead(estimatedLevel, isCharging: true);
            }
            else
            {
                // Post-disconnect settling: skip polling while the device transitions
                // back to wireless mode. Report last known level with IsCharging=false.
                if (_cableDisconnectTime.HasValue &&
                    DateTime.UtcNow - _cableDisconnectTime.Value < PostDisconnectSettlingDelay)
                {
                    _logger.LogDebug(
                        "[MONITOR] Skipping poll — post-disconnect settling ({Elapsed:F1}s / {Delay}s)",
                        (DateTime.UtcNow - _cableDisconnectTime.Value).TotalSeconds,
                        PostDisconnectSettlingDelay.TotalSeconds);
                    // Keep state as Connected with last known level, just not charging
                    if (_lastPositiveLevel > 0)
                        ProcessSuccessfulRead(_lastPositiveLevel, isCharging: false);
                    return Task.CompletedTask;
                }

                // Clear the settling flag once the window has elapsed
                _cableDisconnectTime = null;

                // Normal wireless mode — read from dongle.
                var result = _hidDeviceService.ReadBattery(_activeProfile);
                if (result.Success)
                {
                    _consecutiveFailures = 0;

                    // D2 returns a status packet (e.g. 03-08-02-00) instead of battery %
                    // when the USB cable is plugged in. The byte that looks like a battery
                    // level (0x08 = 8%) is actually a mode/status byte. Guard against this
                    // by re-checking wired presence when the reading looks implausibly low.
                    if (result.BatteryLevel < 10 && _lastPositiveLevel > 0)
                    {
                        bool wiredNow = _hidDeviceService.IsWiredDevicePresent(modelName);
                        if (wiredNow)
                        {
                            _logger.LogDebug(
                                "[D2] Ignoring suspicious low reading ({Raw}%) while USB cable present " +
                                "— using last known level {Last}%",
                                result.BatteryLevel, _lastPositiveLevel);
                            ProcessSuccessfulRead(_lastPositiveLevel, isCharging: true);

                            // Also fix up wired tracking state so subsequent polls use the estimate path
                            if (!_lastWiredPresent)
                            {
                                _lastWiredPresent = true;
                                _chargeStartTime = DateTime.UtcNow;
                                _chargeStartLevel = _lastPositiveLevel;
                            }

                            return Task.CompletedTask;
                        }
                    }

                    ProcessSuccessfulRead(result.BatteryLevel, isCharging: false);
                }
                else
                {
                    ProcessFailedRead();
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error during polling tick");
        }

        return Task.CompletedTask;
    }

    /// <summary>
    /// Estimate the current battery level during charging using a Li-ion charge curve model.
    /// Uses learned charge rate from historical data when available, falling back to defaults.
    /// CC phase (below 80%): constant rate. CV phase (80-100%): rate tapers linearly.
    /// </summary>
    private int EstimateChargingLevel()
    {
        if (_chargeStartTime == null || _chargeStartLevel <= 0)
        {
            // Started app while already charging — no baseline level available.
            // Fall back to last known level (may be 0 if none available).
            return _lastPositiveLevel > 0 ? _lastPositiveLevel : 0;
        }

        double elapsedHours = (DateTime.UtcNow - _chargeStartTime.Value).TotalHours;
        if (elapsedHours <= 0)
            return _chargeStartLevel;

        // Use learned charge rate if available, otherwise fall back to defaults
        double ccRate = CcRatePerHour;
        double cvMax = CvRateMaxPerHour;
        double cvMin = CvRateMinPerHour;

        string deviceKey = _activeProfile?.CompositeKey ?? _activeProfile?.ModelName ?? "";
        var learned = _estimationService.GetLearnedRates(deviceKey);
        if (learned?.ChargeRate > 0)
        {
            ccRate = learned.ChargeRate.Value;
            cvMax = ccRate;
            cvMin = ccRate / 6.0; // CV phase tapers to ~1/6 of CC rate
        }

        // Walk the charge curve in small time steps for accuracy through the CC→CV transition
        double level = _chargeStartLevel;
        double stepHours = 1.0 / 3600.0; // 1-second steps
        double remaining = elapsedHours;

        while (remaining > 0 && level < MaxEstimatedLevel)
        {
            double step = Math.Min(remaining, stepHours);
            double rate;

            if (level < CvThreshold)
            {
                // CC phase: constant rate
                rate = ccRate;
            }
            else
            {
                // CV phase: rate tapers linearly from cvMax at 80% to cvMin at 100%
                double progress = (level - CvThreshold) / (100.0 - CvThreshold);
                rate = cvMax - (cvMax - cvMin) * progress;
            }

            level += rate * step;
            remaining -= step;
        }

        int result = Math.Min((int)Math.Round(level), MaxEstimatedLevel);
        return Math.Max(result, _chargeStartLevel); // Never go below start level
    }

    private void AttemptDeviceDiscovery()
    {
        try
        {
            // Use longer debounce when all probe candidates have been exhausted,
            // to avoid burning 15-20s per poll cycle on known-failing probes.
            TimeSpan debounce = _probeExhausted ? ExhaustedProbeDebounce : ReconnectDebounce;
            if (DateTime.UtcNow - _lastReconnectAttempt < debounce)
                return;

            _lastReconnectAttempt = DateTime.UtcNow;

            // Only show "Connecting" on fresh probe attempts.
            // After exhaustion, stay in NotConnected to avoid a misleading
            // "connecting" → "not connected" loop every poll cycle.
            if (!_probeExhausted)
                UpdateConnectionState(ConnectionState.Connecting);

            // Try cached profiles first
            var savedProfiles = _storageService.LoadProfiles();
            foreach (var profile in savedProfiles)
            {
                var testResult = _hidDeviceService.ReadBattery(profile);
                if (testResult.Success)
                {
                    _probeExhausted = false;
                    _logger.LogInformation("Reconnected using cached profile for {Model}", profile.ModelName);
                    _activeProfile = profile;
                    profile.LastSeen = DateTime.UtcNow;
                    _storageService.SaveProfiles(savedProfiles);
                    return;
                }
            }

            // Full enumeration
            var devices = _hidDeviceService.EnumerateDevices();
            foreach (var device in devices)
            {
                var profile = _hidDeviceService.ProbeDevice(device);
                if (profile != null)
                {
                    _probeExhausted = false;
                    _logger.LogInformation("Discovered device: {Model} via probing", profile.ModelName);
                    _activeProfile = profile;

                    // Save this profile for faster reconnection
                    var profiles = _storageService.LoadProfiles();
                    profiles.RemoveAll(p => p.CompositeKey == profile.CompositeKey);
                    profiles.Add(profile);
                    _storageService.SaveProfiles(profiles);
                    return;
                }
            }

            // If known devices were found but every probe candidate failed, mark exhausted
            // so subsequent polls don't re-enter "Connecting" state and re-run the full chain.
            if (devices.Count > 0 && !_probeExhausted)
            {
                _logger.LogWarning(
                    "[MONITOR] Known device(s) found but no working probe candidate — " +
                    "will retry in {Seconds}s (or on manual rescan)",
                    (int)ExhaustedProbeDebounce.TotalSeconds);
                _probeExhausted = true;
            }

            _logger.LogDebug("No Glorious devices found during enumeration");
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error during device discovery");
        }
    }

    private void ProcessSuccessfulRead(int level, bool isCharging)
    {
        // When the mouse is sleeping (Status 0xA4), the dongle returns Level=0%.
        // Use the last known level instead and track consecutive zero reads.
        int displayLevel = level;
        ConnectionState connection = ConnectionState.Connected;

        if (level == 0 && _lastPositiveLevel > 0)
        {
            _consecutiveZeroReads++;
            _logger.LogDebug("Suppressing transient Level=0% (last known: {Last}%, consecutive zeros: {Count})",
                _lastPositiveLevel, _consecutiveZeroReads);
            displayLevel = _lastPositiveLevel;
            _consecutiveFailures = 0;

            if (_consecutiveZeroReads >= ConsecutiveZeroReadsForSleep)
            {
                connection = ConnectionState.Sleeping;
                _logger.LogInformation("[MONITOR] Mouse detected as sleeping after {Count} consecutive 0% reads",
                    _consecutiveZeroReads);
            }
        }
        else
        {
            _consecutiveZeroReads = 0;
        }

        if (level > 0)
            _lastPositiveLevel = level;

        var previousState = CurrentState;
        string modelName = _activeProfile?.ModelName ?? "Glorious Mouse";
        string deviceKey = _activeProfile?.CompositeKey ?? modelName;
        int criticalThreshold = _settingsService.Current.CriticalBatteryThreshold;

        // Look up stored charge info
        var storedData = _storageService.GetDeviceChargeData(deviceKey);
        DateTime? lastChargeTime = storedData?.LastChargeTime;
        int? lastChargeLevel = storedData?.LastChargeLevel;

        // Update charge time tracking
        if (isCharging)
        {
            lastChargeTime = DateTime.UtcNow;
            lastChargeLevel = displayLevel;
        }

        var newState = new BatteryState
        {
            Level = displayLevel,
            IsCharging = isCharging,
            Connection = connection,
            Health = BatteryState.DeriveHealth(displayLevel, criticalThreshold),
            DeviceName = modelName,
            LastReadTime = DateTime.UtcNow,
            LastChargeTime = lastChargeTime,
            LastChargeLevel = lastChargeLevel
        };

        UpdateState(newState);

        // Update storage
        _storageService.AddBatterySample(deviceKey, displayLevel, isCharging);
        _storageService.UpdateChargeInfo(deviceKey, displayLevel, isCharging ? DateTime.UtcNow : null);

        // Update estimation
        _estimationService.AddSample(deviceKey, displayLevel, isCharging);
        var estimate = _estimationService.GetEstimate(deviceKey);
        UpdateEstimate(estimate);

        // Persist learned rates when charging state changes (session rate just got blended)
        if (previousState.IsCharging != isCharging)
        {
            PersistLearnedRates(deviceKey);
        }

        // Process notifications
        _notificationService.ProcessBatteryUpdate(newState, previousState, _settingsService.Current);
    }

    private void ProcessFailedRead()
    {
        _consecutiveFailures++;
        _logger.LogDebug("Battery read failed. Consecutive failures: {Count}", _consecutiveFailures);

        // CandidateF has intermittent response patterns — use a higher threshold
        // to avoid unnecessary reconnect cycles during normal operation.
        int threshold = (_activeProfile?.PixartMethod == PixartBatteryMethod.CandidateF)
            ? MaxConsecutiveFailuresCandidateF
            : MaxConsecutiveFailuresBeforeReconnect;

        if (_consecutiveFailures >= threshold)
        {
            _logger.LogWarning("Max consecutive failures reached ({Count}). Attempting reconnect.",
                _consecutiveFailures);

            // Transition to LastKnown before full disconnect
            if (CurrentState.Connection == ConnectionState.Connected)
            {
                TransitionToLastKnown();
            }

            _activeProfile = null;
            _consecutiveFailures = 0;
        }
    }

    private void TransitionToDisconnected()
    {
        var previousState = CurrentState;

        var disconnectedState = new BatteryState
        {
            Level = previousState.Level,
            IsCharging = false,
            Connection = ConnectionState.NotConnected,
            Health = previousState.Health,
            DeviceName = previousState.DeviceName,
            LastReadTime = previousState.LastReadTime,
            LastChargeTime = previousState.LastChargeTime,
            LastChargeLevel = previousState.LastChargeLevel
        };

        UpdateState(disconnectedState);
        _notificationService.ProcessBatteryUpdate(disconnectedState, previousState, _settingsService.Current);
    }

    private void TransitionToLastKnown()
    {
        var previousState = CurrentState;

        var lastKnownState = previousState with
        {
            Connection = ConnectionState.LastKnown
        };

        UpdateState(lastKnownState);
    }

    private void UpdateConnectionState(ConnectionState newConnection)
    {
        if (CurrentState.Connection == newConnection)
            return;

        var updated = CurrentState with { Connection = newConnection };
        UpdateState(updated);
    }

    private void UpdateState(BatteryState newState)
    {
        lock (_stateLock)
        {
            CurrentState = newState;
        }

        try
        {
            BatteryStateChanged?.Invoke(newState);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error in BatteryStateChanged event handler");
        }
    }

    private void UpdateEstimate(BatteryEstimate estimate)
    {
        lock (_stateLock)
        {
            CurrentEstimate = estimate;
        }

        try
        {
            EstimateChanged?.Invoke(estimate);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error in EstimateChanged event handler");
        }
    }

    private void TryRestoreCachedProfile()
    {
        try
        {
            var profiles = _storageService.LoadProfiles();
            if (profiles.Count == 0)
                return;

            // Try the most recently seen profile first
            var ordered = profiles.OrderByDescending(p => p.LastSeen);
            foreach (var profile in ordered)
            {
                // First check if the HID device is still present on the system
                if (!_hidDeviceService.IsDevicePresent(profile))
                {
                    _logger.LogDebug(
                        "Cached profile for {Model} not present on system, skipping", profile.ModelName);
                    continue;
                }

                // Try an immediate battery read — works for Sinowealth and some Pixart methods
                var result = _hidDeviceService.ReadBattery(profile);
                if (result.Success)
                {
                    _logger.LogInformation("Restored cached profile for {Model} (read OK)", profile.ModelName);
                    _activeProfile = profile;
                    return;
                }

                // For Pixart passive-read methods (CandidateE, CandidateF, CandidateG), the
                // device won't emit data on cold start until triggered. Trust the saved
                // profile if the device path is present — the normal poll loop will handle reads.
                // Do NOT trust CandidateD: it's synchronous GetFeature, so if ReadBattery
                // failed above it means the response was rejected (false positive filter).
                // Letting CandidateD fall through forces a re-probe that discovers F/G.
                if (profile.Protocol == ChipProtocol.Pixart
                    && profile.PixartMethod != PixartBatteryMethod.CandidateD)
                {
                    _logger.LogInformation(
                        "Restored cached Pixart profile for {Model} (device present, " +
                        "method={Method}, skipping initial read test)",
                        profile.ModelName, profile.PixartMethod);
                    _activeProfile = profile;
                    return;
                }

                _logger.LogDebug(
                    "Cached profile for {Model} (method={Method}) failed read and not eligible " +
                    "for cold-start trust, will re-probe",
                    profile.ModelName, profile.PixartMethod);
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to restore cached profile");
        }
    }

    private void SeedHistoricalRates()
    {
        try
        {
            var chargeData = _storageService.LoadChargeData();
            foreach (var (key, data) in chargeData.Devices)
            {
                if (data.LearnedDischargeRate.HasValue || data.LearnedChargeRate.HasValue)
                {
                    _estimationService.SetHistoricalRates(key,
                        data.LearnedDischargeRate, data.LearnedChargeRate,
                        data.DischargeSessionCount, data.ChargeSessionCount);
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to seed historical rates from storage");
        }
    }

    /// <summary>
    /// On startup, detect if the USB cable (PID 0x824A) is already plugged in.
    /// Seeds _lastWiredPresent, _chargeStartTime, and _chargeStartLevel so the
    /// first PollOnceAsync correctly enters the charging path with a valid baseline.
    /// </summary>
    private void SeedWiredPresenceOnStartup()
    {
        try
        {
            string? modelName = _activeProfile?.ModelName;
            if (string.IsNullOrEmpty(modelName))
                return;

            bool wiredPresent = _hidDeviceService.IsWiredDevicePresent(modelName);
            if (!wiredPresent)
                return;

            _logger.LogInformation(
                "[D2] PID=0x824A detected on startup — USB cable is plugged in");
            _lastWiredPresent = true;

            // Seed charge baseline from persisted data so EstimateChargingLevel
            // has a starting point instead of returning 0%.
            string deviceKey = _activeProfile?.CompositeKey ?? modelName;
            var storedData = _storageService.GetDeviceChargeData(deviceKey);
            int baseline = storedData?.LastKnownLevel ?? 0;

            if (baseline > 0)
            {
                _lastPositiveLevel = baseline;
                _chargeStartLevel = baseline;
                _chargeStartTime = storedData?.LastChargeTime ?? DateTime.UtcNow;
                _logger.LogInformation(
                    "[D2] Seeded charge baseline from storage: {Level}%, chargeStart={Start}",
                    baseline, _chargeStartTime);
            }
            else
            {
                // No stored level — we'll report charging with 0% until real data arrives.
                _chargeStartTime = DateTime.UtcNow;
                _chargeStartLevel = 0;
                _logger.LogWarning(
                    "[D2] USB cable present on startup but no stored battery baseline — " +
                    "charge estimate will start at 0%");
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to seed wired presence on startup");
        }
    }

    private void PersistLearnedRates(string deviceKey)
    {
        try
        {
            var learned = _estimationService.GetLearnedRates(deviceKey);
            if (learned != null)
            {
                _storageService.UpdateLearnedRates(deviceKey,
                    learned.DischargeRate, learned.ChargeRate,
                    learned.DischargeSessionCount, learned.ChargeSessionCount);
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to persist learned rates for device {Key}", deviceKey);
        }
    }

    public void Dispose()
    {
        if (_disposed)
            return;

        _disposed = true;

        try
        {
            _cts?.Cancel();
            _cts?.Dispose();
        }
        catch (ObjectDisposedException)
        {
            // Already disposed
        }

        GC.SuppressFinalize(this);
    }
}
