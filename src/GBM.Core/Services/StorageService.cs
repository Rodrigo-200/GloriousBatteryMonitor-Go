using System.Text.Json;
using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class StorageService : IStorageService
{
    private readonly ILogger<StorageService> _logger;
    private readonly ISettingsService _settingsService;
    private readonly object _chargeDataLock = new();
    private readonly object _profilesLock = new();

    private ChargeData? _cachedChargeData;
    private DateTime _lastChargeDataSaveUtc = DateTime.MinValue;

    private const string ChargeDataFileName = "charge_data.json";
    private const string ProfilesFileName = "conn_profile.json";
    private const int MaxSamplesPerDevice = 200;
    private static readonly TimeSpan MinChargeDataSaveInterval = TimeSpan.FromSeconds(30);

    public StorageService(ILogger<StorageService> logger, ISettingsService settingsService)
    {
        _logger = logger;
        _settingsService = settingsService;
    }

    public ChargeData LoadChargeData()
    {
        lock (_chargeDataLock)
        {
            if (_cachedChargeData != null)
            {
                // Return shallow copy with new Dictionary wrapping same devices
                return new ChargeData { Devices = new Dictionary<string, DeviceChargeData>(_cachedChargeData.Devices) };
            }

            var loaded = LoadFromFile(
                GetChargeDataPath(),
                GbmJsonContext.Default.ChargeData,
                "charge data") ?? new ChargeData();
            _cachedChargeData = loaded;
            return loaded;
        }
    }

    public void SaveChargeData(ChargeData data)
    {
        lock (_chargeDataLock)
        {
            _cachedChargeData = data;
            SaveChargeDataInternal(data, force: true);
        }
    }

    public List<DeviceProfile> LoadProfiles()
    {
        lock (_profilesLock)
        {
            return LoadFromFile(
                GetProfilesPath(),
                GbmJsonContext.Default.ListDeviceProfile,
                "device profiles") ?? new List<DeviceProfile>();
        }
    }

    public void SaveProfiles(List<DeviceProfile> profiles)
    {
        lock (_profilesLock)
        {
            SaveToFile(
                GetProfilesPath(),
                profiles,
                GbmJsonContext.Default.ListDeviceProfile,
                "device profiles");
        }
    }

    public void ClearProfiles()
    {
        lock (_profilesLock)
        {
            try
            {
                var profilesPath = GetProfilesPath();
                if (File.Exists(profilesPath))
                {
                    File.Delete(profilesPath);
                    _logger.LogInformation("Cleared device profiles file");
                }
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to clear device profiles");
            }
        }
    }

    public void AddBatterySample(string deviceKey, int level, bool isCharging)
    {
        lock (_chargeDataLock)
        {
            try
            {
                DateTime now = DateTime.UtcNow;
                var data = LoadChargeDataInternal();
                var deviceData = GetOrCreateDeviceData(data, deviceKey);

                deviceData.Samples.Add(new BatterySample
                {
                    Level = level,
                    Timestamp = now,
                    IsCharging = isCharging
                });

                // Trim to the last MaxSamplesPerDevice samples
                if (deviceData.Samples.Count > MaxSamplesPerDevice)
                {
                    deviceData.Samples = deviceData.Samples
                        .Skip(deviceData.Samples.Count - MaxSamplesPerDevice)
                        .ToList();
                }

                deviceData.LastKnownLevel = level;
                deviceData.LastReadTime = now;

                if (isCharging)
                {
                    deviceData.LastChargeTime = now;
                    deviceData.LastChargeLevel = level;
                }

                _cachedChargeData = data;
                SaveChargeDataInternal(data, force: false);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to add battery sample for device {Key}", deviceKey);
            }
        }
    }

    public DeviceChargeData? GetDeviceChargeData(string deviceKey)
    {
        lock (_chargeDataLock)
        {
            try
            {
                var data = LoadChargeDataInternal();
                return data.Devices.GetValueOrDefault(deviceKey);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to get charge data for device {Key}", deviceKey);
                return null;
            }
        }
    }

    public void UpdateChargeInfo(string deviceKey, int level, DateTime? chargeTime)
    {
        lock (_chargeDataLock)
        {
            try
            {
                var data = LoadChargeDataInternal();
                var deviceData = GetOrCreateDeviceData(data, deviceKey);

                deviceData.LastKnownLevel = level;
                deviceData.LastReadTime = DateTime.UtcNow;

                if (chargeTime.HasValue)
                {
                    deviceData.LastChargeTime = chargeTime.Value;
                    deviceData.LastChargeLevel = level;
                }

                _cachedChargeData = data;
                SaveChargeDataInternal(data, force: false);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to update charge info for device {Key}", deviceKey);
            }
        }
    }

    public void UpdateLearnedRates(string deviceKey, double? dischargeRate, double? chargeRate,
                                    int dischargeSessions, int chargeSessions, bool forceSave = false)
    {
        lock (_chargeDataLock)
        {
            try
            {
                var data = LoadChargeDataInternal();
                var deviceData = GetOrCreateDeviceData(data, deviceKey);

                deviceData.LearnedDischargeRate = dischargeRate;
                deviceData.LearnedChargeRate = chargeRate;
                deviceData.DischargeSessionCount = dischargeSessions;
                deviceData.ChargeSessionCount = chargeSessions;

                _cachedChargeData = data;
                SaveChargeDataInternal(data, force: forceSave);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to update learned rates for device {Key}", deviceKey);
            }
        }
    }

    public void UpdateChargeCalibration(string deviceKey, double? overshootPercent,
                                        int observationCount, bool forceSave = false)
    {
        lock (_chargeDataLock)
        {
            try
            {
                var data = LoadChargeDataInternal();
                var deviceData = GetOrCreateDeviceData(data, deviceKey);

                deviceData.LearnedChargeOvershootPercent = overshootPercent;
                deviceData.ChargeOvershootObservationCount = observationCount;

                _cachedChargeData = data;
                SaveChargeDataInternal(data, force: forceSave);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to update charge calibration for device {Key}", deviceKey);
            }
        }
    }

    private ChargeData LoadChargeDataInternal()
    {
        if (_cachedChargeData != null)
            return _cachedChargeData;

        var loaded = LoadFromFile(
            GetChargeDataPath(),
            GbmJsonContext.Default.ChargeData,
            "charge data") ?? new ChargeData();
        _cachedChargeData = loaded;
        return loaded;
    }

    private void SaveChargeDataInternal(ChargeData data, bool force)
    {
        DateTime now = DateTime.UtcNow;
        bool shouldThrottle = !force &&
                              _lastChargeDataSaveUtc != DateTime.MinValue &&
                              now - _lastChargeDataSaveUtc < MinChargeDataSaveInterval;

        if (shouldThrottle)
        {
            return;
        }

        SaveToFile(
            GetChargeDataPath(),
            data,
            GbmJsonContext.Default.ChargeData,
            "charge data");

        _lastChargeDataSaveUtc = now;
    }

    private T? LoadFromFile<T>(string filePath, System.Text.Json.Serialization.Metadata.JsonTypeInfo<T> typeInfo,
        string description) where T : class
    {
        try
        {
            string actualPath = filePath;

            if (!File.Exists(actualPath))
            {
                // The atomic save writes to .tmp then renames. If the app crashed between
                // deleting the original and renaming the temp file, the data survives in .tmp.
                string tmpPath = filePath + ".tmp";
                if (File.Exists(tmpPath))
                {
                    _logger.LogWarning(
                        "{Description} not found at {Path} but .tmp exists — recovering from interrupted save",
                        description, filePath);
                    try
                    {
                        File.Move(tmpPath, filePath);
                        actualPath = filePath;
                    }
                    catch (Exception moveEx)
                    {
                        _logger.LogWarning(moveEx,
                            "Could not rename .tmp to primary path, reading .tmp directly");
                        actualPath = tmpPath;
                    }
                }
                else
                {
                    _logger.LogDebug("File not found for {Description}: {Path}", description, filePath);
                    return null;
                }
            }

            // Retry read in case the file is briefly locked by the atomic save (Delete+Move)
            string json = "";
            for (int attempt = 1; attempt <= 3; attempt++)
            {
                try
                {
                    json = File.ReadAllText(actualPath);
                    break;
                }
                catch (IOException) when (attempt < 3)
                {
                    Thread.Sleep(50 * attempt);
                }
            }

            var result = JsonSerializer.Deserialize(json, typeInfo);

            if (result == null)
            {
                _logger.LogWarning("{Description} deserialized as null from {Path}", description, actualPath);
            }
            else
            {
                _logger.LogDebug("{Description} loaded successfully from {Path}", description, actualPath);
            }

            return result;
        }
        catch (JsonException ex)
        {
            _logger.LogError(ex,
                "{Description} file is corrupted at {Path}. Backing up and resetting.",
                description, filePath);

            string backupPath = filePath +
                $".corrupted.{DateTime.UtcNow:yyyyMMdd_HHmmss}";
            try
            {
                File.Copy(filePath, backupPath, overwrite: false);
                _logger.LogWarning(
                    "[STORAGE] Corrupted {Description} backed up to {Backup}",
                    description, Path.GetFileName(backupPath));
            }
            catch (Exception backupEx)
            {
                _logger.LogWarning(backupEx,
                    "[STORAGE] Could not create backup of corrupted {Description} file",
                    description);
            }

            if (description == "charge data")
                _cachedChargeData = null;
            TryDeleteFile(filePath);
            return null;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Failed to load {Description} from {Path}", description, filePath);
            return null;
        }
    }

    private void SaveToFile<T>(string filePath, T data,
        System.Text.Json.Serialization.Metadata.JsonTypeInfo<T> typeInfo, string description)
    {
        string json = JsonSerializer.Serialize(data, typeInfo);
        string directory = Path.GetDirectoryName(filePath)!;
        Directory.CreateDirectory(directory);
        string tempPath = filePath + ".tmp";

        // Retry loop: the atomic Delete+Move can race with antivirus or Windows
        // Search Indexer holding a brief lock on the file, causing IOException.
        const int maxRetries = 3;
        for (int attempt = 1; attempt <= maxRetries; attempt++)
        {
            try
            {
                File.WriteAllText(tempPath, json);

                if (File.Exists(filePath))
                    File.Delete(filePath);

                File.Move(tempPath, filePath);

                _logger.LogDebug("{Description} saved to {Path}", description, filePath);
                return;
            }
            catch (IOException ex) when (attempt < maxRetries)
            {
                _logger.LogDebug(
                    "IOException saving {Description} (attempt {Attempt}/{Max}): {Msg}",
                    description, attempt, maxRetries, ex.Message);
                Thread.Sleep(50 * attempt); // 50ms, 100ms backoff
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to save {Description} to {Path}", description, filePath);
                return;
            }
        }
    }

    private static DeviceChargeData GetOrCreateDeviceData(ChargeData data, string deviceKey)
    {
        if (!data.Devices.TryGetValue(deviceKey, out var deviceData))
        {
            deviceData = new DeviceChargeData
            {
                CompositeKey = deviceKey
            };
            data.Devices[deviceKey] = deviceData;
        }

        return deviceData;
    }

    private void TryDeleteFile(string filePath)
    {
        try
        {
            if (File.Exists(filePath))
            {
                File.Delete(filePath);
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to delete corrupted file {Path}", filePath);
        }
    }

    private string GetChargeDataPath() =>
        Path.Combine(_settingsService.GetAppDataPath(), ChargeDataFileName);

    private string GetProfilesPath() =>
        Path.Combine(_settingsService.GetAppDataPath(), ProfilesFileName);
}
