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
    private readonly SemaphoreSlim _pollingTickGate = new(1, 1);

    private readonly object _stateLock = new();
    private CancellationTokenSource? _cts;
    private Task? _pollingTask;
    private DeviceProfile? _activeProfile;
    private int _consecutiveFailures;
    private DateTime _lastReconnectAttempt = DateTime.MinValue;
    private DateTime _lastReconnectTriggerUtc = DateTime.MinValue;
    private DateTime _lastSuccessfulReadUtc = DateTime.MinValue;
    private DateTime _lastAbsenceCheckUtc = DateTime.MinValue;
    private bool _lastAbsenceCheckResult;
    private DateTime _lastLearnedRatesPersistUtc = DateTime.MinValue;
    private int _successfulReadsSinceLearnedPersist;
    private bool _disposed;
    private bool _rescanRequested;

    // Probe exhaustion: when known devices are present but all probe candidates fail,
    // avoid re-entering "Connecting" state every poll cycle (which takes 15-20s per attempt
    // and makes the UI appear stuck on "connecting"). Instead, stay NotConnected and
    // retry the full probe chain less frequently.
    private bool _probeExhausted;

    private TimeSpan _exhaustedProbeDebounce = TimeSpan.FromSeconds(60);
    private const int MaxExhaustedProbeDebounceSeconds = 300;

    // Last valid (non-zero) battery level from a successful read.
    // Used to suppress transient 0% readings when the mouse is sleeping (Status 0xA4).
    private int _lastPositiveLevel;

    // Sleep detection: count consecutive successful reads that return Level=0%.
    // After a threshold, transition to Sleeping instead of staying Connected.
    private int _consecutiveZeroReads;
    private const int ConsecutiveZeroReadsForSleep = 3;

    // Wired-device-based charging: use wired HID presence as the charging signal.
    // While charging, prefer real-time battery readings from the active profile when
    // they look plausible. Fall back to the Li-ion charge curve model when the device
    // reports no data (or clearly bogus low/status values).
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
    private const int SuspiciousChargingLowThreshold = 10;
    private const int SuspiciousChargingDropThreshold = 20;

    private static readonly TimeSpan ReconnectDebounce = TimeSpan.FromMilliseconds(900);
    private static readonly TimeSpan ReconnectCooldown = TimeSpan.FromSeconds(45);
    private static readonly TimeSpan AbsenceEvidenceRecheckInterval = TimeSpan.FromSeconds(15);
    private static readonly TimeSpan MinNoSuccessBeforeReconnect = TimeSpan.FromSeconds(30);
    private static readonly TimeSpan MinNoSuccessBeforeReconnectCandidateF = TimeSpan.FromSeconds(60);
    private static readonly TimeSpan MinNoSuccessBeforeReconnectCandidateG = TimeSpan.FromSeconds(90);
    private static readonly TimeSpan LearnedRatesPersistInterval = TimeSpan.FromMinutes(2);
    private static readonly TimeSpan ExhaustedProbeDebounce = TimeSpan.FromSeconds(60);
    private const int SuccessfulReadsPerLearnedPersist = 6;
    private const int MaxConsecutiveFailuresBeforeReconnect = 3;

    // CandidateF does not always get a response on every poll cycle — the device
    // answers intermittently across RIDs, and each full miss (all 4 RIDs timeout)
    // takes ~12 seconds. A threshold of 3 would trigger reconnect within ~36s even
    // when the device responds every other cycle. Use a higher threshold so transient
    // misses don't cause needless re-probing.
    private const int MaxConsecutiveFailuresCandidateF = 6;

    // CandidateG has a natural 2-fail/1-success heartbeat pattern. The DPI battery
    // check (holding the DPI button) makes the firmware busy driving LED sequences,
    // causing the one successful poll to also fail. A threshold of 3 trips on a
    // single DPI press. Use 10 so only a truly absent device triggers reconnect.
    private const int MaxConsecutiveFailuresCandidateG = 10;

    public BatteryState CurrentState { get; private set; } = BatteryState.Disconnected;
    public BatteryEstimate CurrentEstimate { get; private set; } = BatteryEstimate.Invalid;
    public bool IsRunning => _pollingTask != null && !_pollingTask.IsCompleted;

    public event Action<BatteryState>? BatteryStateChanged;
    public event Action<BatteryEstimate>? EstimateChanged;
    public event Action<string>? ProbeStatusChanged;

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
            PersistLearnedRatesForActiveProfile(force: true);
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

        string? activeDeviceKey = _activeProfile?.CompositeKey;
        if (!string.IsNullOrWhiteSpace(activeDeviceKey))
        {
            _estimationService.Reset(activeDeviceKey);
        }
        else if (!string.IsNullOrWhiteSpace(CurrentState.DeviceName))
        {
            _estimationService.Reset(CurrentState.DeviceName);
        }

        _rescanRequested = true;
        _activeProfile = null;
        _consecutiveFailures = 0;
        _consecutiveZeroReads = 0;
        _lastPositiveLevel = 0;
        _lastSuccessfulReadUtc = DateTime.MinValue;
        _lastReconnectTriggerUtc = DateTime.MinValue;
        _lastAbsenceCheckUtc = DateTime.MinValue;
        _lastAbsenceCheckResult = false;
        _lastLearnedRatesPersistUtc = DateTime.MinValue;
        _successfulReadsSinceLearnedPersist = 0;
        _lastWiredPresent = false;
        _chargeStartTime = null;
        _chargeStartLevel = 0;
        _cableDisconnectTime = null;
        _probeExhausted = false;
        _exhaustedProbeDebounce = TimeSpan.FromSeconds(60);
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
        if (!_pollingTickGate.Wait(0, cancellationToken))
        {
            _logger.LogDebug("[MONITOR] Skipping poll tick because previous tick is still in progress");
            return Task.CompletedTask;
        }

        try
        {
            if (_rescanRequested)
            {
                _rescanRequested = false;
                _activeProfile = null;
                _consecutiveFailures = 0;
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

                // While charging, prefer real telemetry from the active profile.
                // If it is unavailable (or clearly implausible), fall back to the
                // learned Li-ion charge curve estimate.
                if (TryReadRealtimeChargingLevel(out int realtimeLevel))
                {
                    _consecutiveFailures = 0;

                    // Rebase the fallback curve from the latest valid real sample so
                    // future estimate-only ticks continue from a fresh anchor point.
                    _chargeStartTime = DateTime.UtcNow;
                    _chargeStartLevel = realtimeLevel;

                    ProcessSuccessfulRead(realtimeLevel, isCharging: true);
                    return Task.CompletedTask;
                }

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
                        ProcessSuccessfulRead(_lastPositiveLevel, isCharging: false, skipEstimationSample: true);
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
        finally
        {
            _pollingTickGate.Release();
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

    private bool TryReadRealtimeChargingLevel(out int level)
    {
        level = 0;

        if (_activeProfile == null)
            return false;

        var result = _hidDeviceService.ReadBattery(_activeProfile);
        if (!result.Success || result.BatteryLevel <= 0 || result.BatteryLevel > 100)
            return false;

        if (IsSuspiciousChargingReading(result.BatteryLevel))
        {
            _logger.LogDebug(
                "[MONITOR] Ignoring suspicious charging reading {Raw}% (last known {Last}%)",
                result.BatteryLevel,
                _lastPositiveLevel);
            return false;
        }

        level = result.BatteryLevel;
        return true;
    }

    private bool IsSuspiciousChargingReading(int level)
    {
        if (_lastPositiveLevel <= 0)
            return false;

        bool implausiblyLow = level < SuspiciousChargingLowThreshold &&
                              _lastPositiveLevel >= SuspiciousChargingDropThreshold;
        bool implausibleDrop = (_lastPositiveLevel - level) >= SuspiciousChargingDropThreshold;

        return implausiblyLow || implausibleDrop;
    }

    private void AttemptDeviceDiscovery()
    {
        try
        {
            // Use longer debounce when all probe candidates have been exhausted,
            // to avoid burning 15-20s per poll cycle on known-failing probes.
            TimeSpan debounce = _probeExhausted ? _exhaustedProbeDebounce : ReconnectDebounce;
            if (DateTime.UtcNow - _lastReconnectAttempt < debounce)
                return;

            _lastReconnectAttempt = DateTime.UtcNow;

            // Only show "Connecting" on fresh probe attempts.
            // After exhaustion, stay in NotConnected to avoid a misleading
            // "connecting" → "not connected" loop every poll cycle.
            if (!_probeExhausted)
                UpdateConnectionState(ConnectionState.Connecting);

            ReportProbeStatus("Scanning for Glorious devices...");

            // Try cached profiles first
            var savedProfiles = _storageService.LoadProfiles();
            foreach (var profile in savedProfiles)
            {
                // For CandidateF/G, a single ReadBattery attempt often fails due to
                // the intermittent response pattern (2 fail / 1 success). Retry up to
                // 5 times with a short delay before giving up on the saved profile.
                bool isIntermittent = profile.Protocol == ChipProtocol.Pixart
                    && (profile.PixartMethod == PixartBatteryMethod.CandidateF
                        || profile.PixartMethod == PixartBatteryMethod.CandidateG);

                int maxAttempts = isIntermittent ? 5 : 1;

                if (isIntermittent && maxAttempts > 1)
                {
                    _logger.LogInformation(
                        "[MONITOR] Consecutive failure threshold reached — retrying saved {Method} profile before full rescan",
                        profile.PixartMethod);
                }

                for (int attempt = 1; attempt <= maxAttempts; attempt++)
                {
                    var testResult = _hidDeviceService.ReadBattery(profile);
                    if (testResult.Success)
                    {
                        _probeExhausted = false;
                        _exhaustedProbeDebounce = TimeSpan.FromSeconds(60);
                        _logger.LogInformation("Reconnected using cached profile for {Model} (attempt {Attempt}/{Max})",
                            profile.ModelName, attempt, maxAttempts);
                        ReportProbeStatus($"Found {profile.ModelName}");
                        _activeProfile = profile;
                        profile.LastSeen = DateTime.UtcNow;
                        _storageService.SaveProfiles(savedProfiles);
                        return;
                    }

                    if (attempt < maxAttempts)
                    {
                        _logger.LogDebug(
                            "[MONITOR] Saved profile retry {Attempt}/{Max} failed for {Model}, waiting 5s...",
                            attempt, maxAttempts, profile.ModelName);
                        Thread.Sleep(5000);
                    }
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
                    _exhaustedProbeDebounce = TimeSpan.FromSeconds(60);
                    _logger.LogInformation("Discovered device: {Model} via probing", profile.ModelName);
                    ReportProbeStatus($"Found {profile.ModelName}");
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
                    (int)_exhaustedProbeDebounce.TotalSeconds);
                _probeExhausted = true;
                // Double the debounce, capped at 300s
                _exhaustedProbeDebounce = TimeSpan.FromSeconds(
                    Math.Min(_exhaustedProbeDebounce.TotalSeconds * 2, MaxExhaustedProbeDebounceSeconds));
            }

            _logger.LogDebug("No Glorious devices found during enumeration");
            ReportProbeStatus("No Glorious device found");
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error during device discovery");
        }
    }

    private void ProcessSuccessfulRead(int level, bool isCharging, bool skipEstimationSample = false)
    {
        _lastSuccessfulReadUtc = DateTime.UtcNow;
        _lastAbsenceCheckResult = false;
        _consecutiveFailures = 0;

        // When the mouse is sleeping (Status 0xA4), the dongle returns Level=0%.
        // Use the last known level instead and track consecutive zero reads.
        int displayLevel = level;
        ConnectionState connection = ConnectionState.Connected;
        bool suppressedSleepRead = false;

        if (level == 0 && _lastPositiveLevel > 0)
        {
            suppressedSleepRead = true;
            _consecutiveZeroReads++;
            displayLevel = _lastPositiveLevel;
            _consecutiveFailures = 0;

            if (_consecutiveZeroReads >= ConsecutiveZeroReadsForSleep)
            {
                connection = ConnectionState.Sleeping;
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

        if (previousState.Connection != ConnectionState.Sleeping && connection == ConnectionState.Sleeping)
        {
            _logger.LogInformation(
                "[MONITOR] Mouse detected as sleeping after {Count} consecutive 0% reads",
                _consecutiveZeroReads);
        }
        else if (previousState.Connection == ConnectionState.Sleeping && connection == ConnectionState.Connected)
        {
            _logger.LogInformation("[MONITOR] Mouse woke from sleep state");
        }

        // Update storage
        _storageService.AddBatterySample(deviceKey, displayLevel, isCharging);
        _storageService.UpdateChargeInfo(deviceKey, displayLevel, isCharging ? DateTime.UtcNow : null);

        // Update estimation
        bool wakingFromSleep = previousState.Connection == ConnectionState.Sleeping &&
                               connection == ConnectionState.Connected;
        bool shouldSkipEstimationSample = skipEstimationSample ||
                                          suppressedSleepRead ||
                                          connection == ConnectionState.Sleeping ||
                                          wakingFromSleep;

        if (!shouldSkipEstimationSample)
        {
            _estimationService.AddSample(deviceKey, displayLevel, isCharging);
        }
        var estimate = _estimationService.GetEstimate(deviceKey);
        UpdateEstimate(estimate);
        _successfulReadsSinceLearnedPersist++;

        // Persist learned rates when charging state changes (session rate just got blended)
        if (previousState.IsCharging != isCharging)
        {
            PersistLearnedRates(deviceKey, force: true);
        }
        else if (_successfulReadsSinceLearnedPersist >= SuccessfulReadsPerLearnedPersist ||
                 DateTime.UtcNow - _lastLearnedRatesPersistUtc >= LearnedRatesPersistInterval)
        {
            PersistLearnedRates(deviceKey, force: false);
        }

        // Process notifications
        _notificationService.ProcessBatteryUpdate(newState, previousState, _settingsService.Current);
    }

    private void ProcessFailedRead()
    {
        _consecutiveFailures++;
        _logger.LogDebug("Battery read failed. Consecutive failures: {Count}", _consecutiveFailures);

        // CandidateF and CandidateG have intermittent response patterns — use higher
        // thresholds to avoid unnecessary reconnect cycles during normal operation.
        int threshold = (_activeProfile?.PixartMethod) switch
        {
            PixartBatteryMethod.CandidateF => MaxConsecutiveFailuresCandidateF,
            PixartBatteryMethod.CandidateG => MaxConsecutiveFailuresCandidateG,
            _ => MaxConsecutiveFailuresBeforeReconnect
        };

        if (_consecutiveFailures >= threshold)
        {
            TimeSpan noSuccessWindow = (_activeProfile?.PixartMethod) switch
            {
                PixartBatteryMethod.CandidateF => MinNoSuccessBeforeReconnectCandidateF,
                PixartBatteryMethod.CandidateG => MinNoSuccessBeforeReconnectCandidateG,
                _ => MinNoSuccessBeforeReconnect
            };

            bool noRecentSuccess = _lastSuccessfulReadUtc == DateTime.MinValue ||
                                   DateTime.UtcNow - _lastSuccessfulReadUtc >= noSuccessWindow;
            if (!noRecentSuccess)
            {
                _logger.LogInformation(
                    "[MONITOR] Reconnect gated: recent successful read within {Window}s",
                    noSuccessWindow.TotalSeconds);
                return;
            }

            bool hasAbsenceEvidence = HasCorroboratedAbsenceEvidence();
            if (!hasAbsenceEvidence)
            {
                _logger.LogInformation(
                    "[MONITOR] Reconnect gated: failure threshold met but device path is still present");
                return;
            }

            if (DateTime.UtcNow - _lastReconnectTriggerUtc < ReconnectCooldown)
            {
                _logger.LogInformation(
                    "[MONITOR] Reconnect gated by cooldown ({Cooldown}s)",
                    ReconnectCooldown.TotalSeconds);
                return;
            }

            _logger.LogWarning(
                "Max consecutive failures reached ({Count}) with corroborated absence evidence. Attempting reconnect.",
                _consecutiveFailures);

            // Transition to LastKnown before full disconnect
            if (CurrentState.Connection == ConnectionState.Connected ||
                CurrentState.Connection == ConnectionState.Sleeping)
            {
                TransitionToLastKnown();
            }

            _lastReconnectTriggerUtc = DateTime.UtcNow;
            _activeProfile = null;
            _consecutiveFailures = 0;
        }
    }

    private bool HasCorroboratedAbsenceEvidence()
    {
        if (_activeProfile == null)
            return true;

        if (!_hidDeviceService.IsDevicePresent(_activeProfile))
            return true;

        if (DateTime.UtcNow - _lastAbsenceCheckUtc < AbsenceEvidenceRecheckInterval)
            return _lastAbsenceCheckResult;

        bool noMatchingDevice = true;
        try
        {
            var devices = _hidDeviceService.EnumerateDevices();
            noMatchingDevice = !devices.Any(d =>
                string.Equals(d.CompositeKey, _activeProfile.CompositeKey, StringComparison.OrdinalIgnoreCase));
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[MONITOR] Could not corroborate absence via re-enumeration");
            noMatchingDevice = false;
        }

        _lastAbsenceCheckUtc = DateTime.UtcNow;
        _lastAbsenceCheckResult = noMatchingDevice;
        return noMatchingDevice;
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

    private void ReportProbeStatus(string message)
    {
        try { ProbeStatusChanged?.Invoke(message); }
        catch { }
    }

    private void TryRestoreCachedProfile()
    {
        try
        {
            ReportProbeStatus("Checking saved device...");
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
                    ReportProbeStatus($"Reconnecting to {profile.ModelName}...");
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
                    // For CandidateF, verify the sibling input interface (col05) still exists.
                    // If the sibling is missing (e.g. dongle plugged into different USB port),
                    // the profile is stale and should trigger a re-probe instead.
                    if (profile.PixartMethod == PixartBatteryMethod.CandidateF
                        && !string.IsNullOrEmpty(profile.SiblingDevicePath)
                        && !_hidDeviceService.IsDevicePresent(new DeviceProfile
                        {
                            DevicePath = profile.SiblingDevicePath,
                            VendorId = profile.VendorId,
                            ProductId = profile.ProductId,
                            Protocol = ChipProtocol.Pixart
                        }))
                    {
                        _logger.LogDebug(
                            "Cached CandidateF profile for {Model}: sibling input interface " +
                            "not found at {SiblingPath}, will re-probe",
                            profile.ModelName, profile.SiblingDevicePath);
                        continue;
                    }

                    _logger.LogInformation(
                        "Restored cached Pixart profile for {Model} (device present, " +
                        "method={Method}, skipping initial read test)",
                        profile.ModelName, profile.PixartMethod);
                    ReportProbeStatus($"Reconnecting to {profile.ModelName}...");
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

    private void PersistLearnedRatesForActiveProfile(bool force)
    {
        string? key = _activeProfile?.CompositeKey;
        if (!string.IsNullOrWhiteSpace(key))
        {
            PersistLearnedRates(key, force);
        }
    }

    private void PersistLearnedRates(string deviceKey, bool force)
    {
        try
        {
            var learned = _estimationService.GetLearnedRates(deviceKey);
            if (learned != null)
            {
                _storageService.UpdateLearnedRates(deviceKey,
                    learned.DischargeRate, learned.ChargeRate,
                    learned.DischargeSessionCount, learned.ChargeSessionCount);
                _lastLearnedRatesPersistUtc = DateTime.UtcNow;
                _successfulReadsSinceLearnedPersist = 0;
                if (force)
                {
                    _logger.LogDebug("Persisted learned rates for {Key} (forced)", deviceKey);
                }
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
            PersistLearnedRatesForActiveProfile(force: true);
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
