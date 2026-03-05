using System.Runtime.InteropServices;
using System.Text.Json;
using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class SettingsService : ISettingsService
{
    private readonly ILogger<SettingsService> _logger;
    private readonly object _lock = new();
    private readonly string _appDataPath;
    private readonly string _settingsFilePath;

    private AppSettings _current = new();

    public AppSettings Current
    {
        get
        {
            lock (_lock)
            {
                return _current;
            }
        }
    }

    public event Action<AppSettings>? SettingsChanged;

    public SettingsService(ILogger<SettingsService> logger)
    {
        _logger = logger;
        _appDataPath = ComputeAppDataPath();
        _settingsFilePath = Path.Combine(_appDataPath, "settings.json");
        Load();
    }

    public string GetAppDataPath()
    {
        return _appDataPath;
    }

    public void Load()
    {
        lock (_lock)
        {
            try
            {
                if (!File.Exists(_settingsFilePath))
                {
                    _logger.LogInformation("Settings file not found at {Path}. Using defaults.", _settingsFilePath);
                    _current = new AppSettings();
                    ApplyEnvironmentOverrides();
                    return;
                }

                string json = File.ReadAllText(_settingsFilePath);
                var loaded = JsonSerializer.Deserialize(json, GbmJsonContext.Default.AppSettings);

                if (loaded != null)
                {
                    _current = loaded;
                    _logger.LogInformation("Settings loaded from {Path}", _settingsFilePath);
                }
                else
                {
                    _logger.LogWarning("Settings file deserialized as null. Using defaults.");
                    _current = new AppSettings();
                }
            }
            catch (JsonException ex)
            {
                _logger.LogError(ex, "Settings file is corrupted. Resetting to defaults.");
                _current = new AppSettings();
                TrySaveInternal(_current);
            }
            catch (Exception ex)
            {
                _logger.LogError(ex, "Failed to load settings. Using defaults.");
                _current = new AppSettings();
            }

            ApplyEnvironmentOverrides();
        }
    }

    public void Save(AppSettings settings)
    {
        lock (_lock)
        {
            TrySaveInternal(settings);
            _current = settings.Clone();
        }

        // Fire event outside of lock to avoid deadlocks
        try
        {
            SettingsChanged?.Invoke(settings);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Error in SettingsChanged event handler");
        }
    }

    private void TrySaveInternal(AppSettings settings)
    {
        try
        {
            // Ensure directory exists
            Directory.CreateDirectory(_appDataPath);

            string json = JsonSerializer.Serialize(settings, GbmJsonContext.Default.AppSettings);

            // Atomic write: write to temp file, then rename
            string tempPath = _settingsFilePath + ".tmp";
            File.WriteAllText(tempPath, json);

            // On Windows, File.Move with overwrite requires .NET 6+
            if (File.Exists(_settingsFilePath))
            {
                File.Delete(_settingsFilePath);
            }

            File.Move(tempPath, _settingsFilePath);

            _logger.LogDebug("Settings saved to {Path}", _settingsFilePath);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Failed to save settings to {Path}", _settingsFilePath);
        }
    }

    private void ApplyEnvironmentOverrides()
    {
        try
        {
            string? safeMode = Environment.GetEnvironmentVariable("GBM_SAFE_MODE");
            if (!string.IsNullOrEmpty(safeMode))
            {
                if (safeMode.Equals("1", StringComparison.Ordinal) ||
                    safeMode.Equals("true", StringComparison.OrdinalIgnoreCase))
                {
                    _logger.LogInformation("GBM_SAFE_MODE environment variable detected. Forcing SafeHidMode=true.");
                    _current.SafeHidMode = true;
                }
                else if (safeMode.Equals("0", StringComparison.Ordinal) ||
                         safeMode.Equals("false", StringComparison.OrdinalIgnoreCase))
                {
                    _logger.LogInformation("GBM_SAFE_MODE environment variable detected. Forcing SafeHidMode=false.");
                    _current.SafeHidMode = false;
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Failed to read GBM_SAFE_MODE environment variable");
        }
    }

    private static string ComputeAppDataPath()
    {
        if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
        {
            string appData = Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
            return Path.Combine(appData, "GloriousBatteryMonitor");
        }

        if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
        {
            string home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            return Path.Combine(home, "Library", "Application Support", "GloriousBatteryMonitor");
        }

        // Linux / other Unix
        {
            string? xdgConfig = Environment.GetEnvironmentVariable("XDG_CONFIG_HOME");
            if (!string.IsNullOrEmpty(xdgConfig))
            {
                return Path.Combine(xdgConfig, "GloriousBatteryMonitor");
            }

            string home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            return Path.Combine(home, ".config", "GloriousBatteryMonitor");
        }
    }
}
