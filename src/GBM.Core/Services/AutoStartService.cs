using System.Diagnostics.CodeAnalysis;
using System.Runtime.InteropServices;
using Microsoft.Extensions.Logging;
using Microsoft.Win32;

namespace GBM.Core.Services;

[SuppressMessage("Interoperability", "CA1416:Validate platform compatibility")]
public class AutoStartService : IAutoStartService
{
    private readonly ILogger<AutoStartService> _logger;

    private const string AppName = "GloriousBatteryMonitor";
    private const string WindowsRegistryKey = @"SOFTWARE\Microsoft\Windows\CurrentVersion\Run";
    private const string MacOsLaunchAgentName = "com.glorious.batterymonitor.plist";
    private const string LinuxDesktopFileName = "glorious-battery-monitor.desktop";

    public AutoStartService(ILogger<AutoStartService> logger)
    {
        _logger = logger;
    }

    public bool IsEnabled()
    {
        try
        {
            if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
                return IsEnabledWindows();
            if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
                return IsEnabledMacOs();
            if (RuntimeInformation.IsOSPlatform(OSPlatform.Linux))
                return IsEnabledLinux();

            _logger.LogWarning("Auto-start check not supported on this platform");
            return false;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Failed to check auto-start status");
            return false;
        }
    }

    public void SetAutoStart(bool enabled)
    {
        try
        {
            if (RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                SetAutoStartWindows(enabled);
            }
            else if (RuntimeInformation.IsOSPlatform(OSPlatform.OSX))
            {
                SetAutoStartMacOs(enabled);
            }
            else if (RuntimeInformation.IsOSPlatform(OSPlatform.Linux))
            {
                SetAutoStartLinux(enabled);
            }
            else
            {
                _logger.LogWarning("Auto-start not supported on this platform");
            }
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "Failed to set auto-start to {Enabled}", enabled);
        }
    }

    // ===== Windows =====

    private bool IsEnabledWindows()
    {
        using var key = Registry.CurrentUser.OpenSubKey(WindowsRegistryKey, false);
        var value = key?.GetValue(AppName) as string;
        return !string.IsNullOrEmpty(value);
    }

    private void SetAutoStartWindows(bool enabled)
    {
        using var key = Registry.CurrentUser.OpenSubKey(WindowsRegistryKey, true);
        if (key == null)
        {
            _logger.LogError("Failed to open Windows registry key for auto-start");
            return;
        }

        if (enabled)
        {
            string exePath = GetExecutablePath();
            key.SetValue(AppName, $"\"{exePath}\"");
            _logger.LogInformation("Windows auto-start enabled: {Path}", exePath);
        }
        else
        {
            key.DeleteValue(AppName, false);
            _logger.LogInformation("Windows auto-start disabled");
        }
    }

    // ===== macOS =====

    private bool IsEnabledMacOs()
    {
        string plistPath = GetMacOsLaunchAgentPath();
        return File.Exists(plistPath);
    }

    private void SetAutoStartMacOs(bool enabled)
    {
        string plistPath = GetMacOsLaunchAgentPath();

        if (enabled)
        {
            string exePath = GetExecutablePath();
            string plistDir = Path.GetDirectoryName(plistPath)!;
            Directory.CreateDirectory(plistDir);

            string plistContent = $"""
                <?xml version="1.0" encoding="UTF-8"?>
                <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
                <plist version="1.0">
                <dict>
                    <key>Label</key>
                    <string>{AppName}</string>
                    <key>ProgramArguments</key>
                    <array>
                        <string>{exePath}</string>
                    </array>
                    <key>RunAtLoad</key>
                    <true/>
                    <key>KeepAlive</key>
                    <false/>
                </dict>
                </plist>
                """;

            File.WriteAllText(plistPath, plistContent);
            _logger.LogInformation("macOS LaunchAgent created at {Path}", plistPath);
        }
        else
        {
            if (File.Exists(plistPath))
            {
                File.Delete(plistPath);
                _logger.LogInformation("macOS LaunchAgent removed from {Path}", plistPath);
            }
        }
    }

    private static string GetMacOsLaunchAgentPath()
    {
        string home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
        return Path.Combine(home, "Library", "LaunchAgents", MacOsLaunchAgentName);
    }

    // ===== Linux =====

    private bool IsEnabledLinux()
    {
        string desktopPath = GetLinuxAutostartPath();
        return File.Exists(desktopPath);
    }

    private void SetAutoStartLinux(bool enabled)
    {
        string desktopPath = GetLinuxAutostartPath();

        if (enabled)
        {
            string exePath = GetExecutablePath();
            string autostartDir = Path.GetDirectoryName(desktopPath)!;
            Directory.CreateDirectory(autostartDir);

            string desktopContent = $"""
                [Desktop Entry]
                Type=Application
                Name=Glorious Battery Monitor
                Comment=Monitor battery level for Glorious wireless mice
                Exec={exePath}
                Icon=glorious-battery-monitor
                Terminal=false
                Categories=Utility;
                StartupNotify=false
                X-GNOME-Autostart-enabled=true
                """;

            File.WriteAllText(desktopPath, desktopContent);
            _logger.LogInformation("Linux autostart desktop entry created at {Path}", desktopPath);
        }
        else
        {
            if (File.Exists(desktopPath))
            {
                File.Delete(desktopPath);
                _logger.LogInformation("Linux autostart desktop entry removed from {Path}", desktopPath);
            }
        }
    }

    private static string GetLinuxAutostartPath()
    {
        string? xdgConfig = Environment.GetEnvironmentVariable("XDG_CONFIG_HOME");
        string configDir;

        if (!string.IsNullOrEmpty(xdgConfig))
        {
            configDir = xdgConfig;
        }
        else
        {
            string home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            configDir = Path.Combine(home, ".config");
        }

        return Path.Combine(configDir, "autostart", LinuxDesktopFileName);
    }

    // ===== Shared =====

    private static string GetExecutablePath()
    {
        return Environment.ProcessPath ?? AppContext.BaseDirectory;
    }
}
