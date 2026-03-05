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

    private const string ChargeDataFileName = "charge_data.json";
    private const string ProfilesFileName = "conn_profile.json";
    private const int MaxSamplesPerDevice = 200;

    public StorageService(ILogger<StorageService> logger, ISettingsService settingsService)
    {
        _logger = logger;
        _settingsService = settingsService;
    }

    public ChargeData LoadChargeData()
    {
        lock (_chargeDataLock)
        {
            return LoadFromFile(
                GetChargeDataPath(),
                GbmJsonContext.Default.ChargeData,
                "charge data") ?? new ChargeData();
        }
    }

    public void SaveChargeData(ChargeData data)
    {
        lock (_chargeDataLock)
        {
            SaveToFile(
                GetChargeDataPath(),
                data,
                GbmJsonContext.Default.ChargeData,
                "charge data");
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

    public void AddBatterySample(string deviceKey, int level, bool isCharging)
    {
        lock (_chargeDataLock)
        {
            try
            {
                var data = LoadChargeDataInternal();
                var deviceData = GetOrCreateDeviceData(data, deviceKey);

                deviceData.Samples.Add(new BatterySample
                {
                    Level = level,
                    Timestamp = DateTime.UtcNow,
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
                deviceData.LastReadTime = DateTime.UtcNow;

                SaveChargeDataInternal(data);
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

                SaveChargeDataInternal(data);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to update charge info for device {Key}", deviceKey);
            }
        }
    }

    public void UpdateLearnedRates(string deviceKey, double? dischargeRate, double? chargeRate,
                                    int dischargeSessions, int chargeSessions)
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

                SaveChargeDataInternal(data);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to update learned rates for device {Key}", deviceKey);
            }
        }
    }

    private ChargeData LoadChargeDataInternal()
    {
        return LoadFromFile(
            GetChargeDataPath(),
            GbmJsonContext.Default.ChargeData,
            "charge data") ?? new ChargeData();
    }

    private void SaveChargeDataInternal(ChargeData data)
    {
        SaveToFile(
            GetChargeDataPath(),
            data,
            GbmJsonContext.Default.ChargeData,
            "charge data");
    }

    private T? LoadFromFile<T>(string filePath, System.Text.Json.Serialization.Metadata.JsonTypeInfo<T> typeInfo,
        string description) where T : class
    {
        try
        {
            if (!File.Exists(filePath))
            {
                _logger.LogDebug("File not found for {Description}: {Path}", description, filePath);
                return null;
            }

            string json = File.ReadAllText(filePath);
            var result = JsonSerializer.Deserialize(json, typeInfo);

            if (result == null)
            {
                _logger.LogWarning("{Description} deserialized as null from {Path}", description, filePath);
            }

            return result;
        }
        catch (JsonException ex)
        {
            _logger.LogError(ex, "{Description} file is corrupted at {Path}. Resetting to default.", description,
                filePath);
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
        try
        {
            string directory = Path.GetDirectoryName(filePath)!;
            Directory.CreateDirectory(directory);

            string json = JsonSerializer.Serialize(data, typeInfo);

            // Atomic write: write to temp file, then rename
            string tempPath = filePath + ".tmp";
            File.WriteAllText(tempPath, json);

            if (File.Exists(filePath))
            {
                File.Delete(filePath);
            }

            File.Move(tempPath, filePath);

            _logger.LogDebug("{Description} saved to {Path}", description, filePath);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Failed to save {Description} to {Path}", description, filePath);
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
