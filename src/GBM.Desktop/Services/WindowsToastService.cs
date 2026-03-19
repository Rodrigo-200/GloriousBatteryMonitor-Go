using GBM.Core.Models;
using Microsoft.Extensions.Logging;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;

namespace GBM.Desktop.Services;

/// <summary>
/// Delivers battery alerts as Windows 11 Action Center toasts
/// with rendered circular icons, contextual copy, and appropriate audio.
/// Uses PowerShell with Windows 10+ built-in Toast APIs.
/// </summary>
public class WindowsToastService : IDisposable
{
    private readonly ILogger<WindowsToastService> _logger;
    private bool _disposed;
    private bool _shortcutCreated = false;

    public WindowsToastService(ILogger<WindowsToastService> logger)
    {
        _logger = logger;
        // Initialize: Create Start Menu shortcut and pre-render icons
        Task.Run(async () =>
        {
            await Task.Delay(100); // Let app finish startup
            EnsureStartMenuShortcut();
            PreRenderIcons();
        });
    }

    public void ShowNotification(NotificationType type, string title, string message)
    {
        if (_disposed) return;

        try
        {
            // Validate inputs
            if (string.IsNullOrWhiteSpace(title) || string.IsNullOrWhiteSpace(message))
            {
                _logger.LogWarning("[TOAST] Notification missing title or message");
                return;
            }

            string? iconUri = NotificationIconRenderer.GetIconUri(type);
            string audioSrc = GetAudioSource(type);
            string bodyText = GetContextualBody(type, message);

            // Display via PowerShell (reliable on Windows 11 for non-MSIX apps)
            _ = Task.Run(() => ShowToastViaPowerShell(title, bodyText, iconUri, audioSrc, type));

            _logger.LogInformation("[TOAST] Delivered [{Type}]: {Title}", type, title);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex,
                "[TOAST] Failed to show notification [{Type}]: {Title}", type, title);
        }
    }

    private void EnsureStartMenuShortcut()
    {
        if (_shortcutCreated) return;

        try
        {
            // Use AppContext.BaseDirectory for single-file apps
            string appPath = Process.GetCurrentProcess().MainModule?.FileName ??
                            Path.Combine(AppContext.BaseDirectory, "GBM.Desktop.exe");

            string startMenuPath = Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData),
                "Microsoft", "Windows", "Start Menu", "Programs");

            if (!Directory.Exists(startMenuPath))
            {
                _logger.LogWarning("[TOAST] Start Menu folder not found, skipping shortcut creation");
                return;
            }

            string shortcutPath = Path.Combine(startMenuPath, "Glorious Battery Monitor.lnk");

            // Only create if it doesn't exist
            if (!File.Exists(shortcutPath) && !string.IsNullOrEmpty(appPath))
            {
                CreateShortcut(appPath, shortcutPath, "Glorious Battery Monitor", "Monitor your wireless mouse battery");
                _logger.LogInformation("[TOAST] Created Start Menu shortcut for app registration");
            }

            _shortcutCreated = true;
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[TOAST] Failed to create Start Menu shortcut (toasts may still work)");
        }
    }

    private void CreateShortcut(string targetPath, string shortcutPath, string name, string description)
    {
        try
        {
            // Use WScript.Shell COM object to create shortcut
            var shellType = Type.GetTypeFromProgID("WScript.Shell");
            if (shellType == null)
            {
                _logger.LogWarning("[TOAST] WScript.Shell not available (COM not working)");
                return;
            }

            dynamic? shell = Activator.CreateInstance(shellType);
            if (shell == null) return;

            try
            {
                dynamic? shortcut = shell.CreateShortcut(shortcutPath);
                if (shortcut == null) return;

                try
                {
                    shortcut.TargetPath = targetPath;
                    shortcut.WorkingDirectory = Path.GetDirectoryName(targetPath);
                    shortcut.Description = description;
                    shortcut.IconLocation = targetPath;
                    shortcut.Save();
                }
                finally
                {
                    if (shortcut != null) Marshal.FinalReleaseComObject(shortcut);
                }
            }
            finally
            {
                if (shell != null) Marshal.FinalReleaseComObject(shell);
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[TOAST] Failed to create shortcut via WScript.Shell");
        }
    }

    private void ShowToastViaPowerShell(string title, string message, string? iconUri, string audioSrc, NotificationType type)
    {
        try
        {
            // Build the XML template with icon if available
            string logoXml = iconUri != null
                ? $@"<image placement=""appLogoOverride"" hint-crop=""circle"" src=""{EscapeXml(iconUri)}""/>"
                : string.Empty;

            string toastXml = $@"<toast duration=""short""><visual><binding template=""ToastGeneric"">{logoXml}<text hint-maxLines=""1"">{EscapeXml(title)}</text><text>{EscapeXml(message)}</text><text placement=""attribution"">Glorious Battery Monitor</text></binding></visual><audio src=""{audioSrc}"" loop=""false""/></toast>";

            // Escape the XML for PowerShell single-quote wrapping
            string escapedXml = toastXml.Replace("'", "''");

            // Build PowerShell script - using single quotes to avoid most escaping issues
            string psScript = @"
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$APP_ID = 'GloriousBatteryMonitor.App'
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('" + escapedXml + @"')
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
$toast.Tag = 'battery'
$toast.Group = 'battery'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($APP_ID).Show($toast)
";

            var psi = new ProcessStartInfo
            {
                FileName = "powershell.exe",
                Arguments = $"-NoProfile -Command \"{EscapeForPowerShellCmdLine(psScript)}\"",
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8,
                StandardErrorEncoding = Encoding.UTF8
            };

            using (var process = Process.Start(psi))
            {
                if (process == null)
                {
                    _logger.LogWarning("[TOAST] Failed to start PowerShell process");
                    return;
                }

                bool exited = process.WaitForExit(5000);

                if (!exited)
                {
                    try { process.Kill(); } catch { }
                    _logger.LogWarning("[TOAST] PowerShell command timed out");
                    return;
                }

                // Capture any output for debugging
                string output = process.StandardOutput.ReadToEnd().Trim();
                string errors = process.StandardError.ReadToEnd().Trim();

                if (!string.IsNullOrEmpty(output))
                    _logger.LogDebug("[TOAST] PowerShell stdout: {Output}", output);

                if (!string.IsNullOrEmpty(errors))
                    _logger.LogWarning("[TOAST] PowerShell stderr: {Error}", errors);

                if (process.ExitCode != 0)
                    _logger.LogWarning("[TOAST] PowerShell exit code: {ExitCode}", process.ExitCode);
                else
                    _logger.LogDebug("[TOAST] PowerShell executed successfully");
            }
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[TOAST] Exception displaying toast for type {Type}", type);
        }
    }

    private static string EscapeForPowerShellCmdLine(string value)
    {
        // Escape for PowerShell -Command parameter
        return value.Replace("\"", "\\\"");
    }

    private static string GetContextualBody(NotificationType type, string originalMessage) =>
        type switch
        {
            NotificationType.Critical =>
                "Plug in your mouse soon to avoid losing connection mid-session.",
            NotificationType.Low =>
                "Your mouse battery is getting low. Consider charging soon.",
            NotificationType.FullCharge =>
                "You can safely unplug your mouse — it's fully charged.",
            NotificationType.Disconnected =>
                "The wireless receiver lost contact. Check USB and try rescanning.",
            _ => originalMessage
        };

    private static string GetAudioSource(NotificationType type) =>
        type switch
        {
            NotificationType.Critical => "ms-winsoundevent:Notification.Looping.Alarm2",
            NotificationType.Low      => "ms-winsoundevent:Notification.Default",
            _                         => "ms-winsoundevent:Notification.Default"
        };

    private static string EscapeXml(string value) =>
        value
            .Replace("&", "&amp;")
            .Replace("<", "&lt;")
            .Replace(">", "&gt;")
            .Replace("\"", "&quot;")
            .Replace("'", "&apos;");

    private static void PreRenderIcons()
    {
        foreach (NotificationType type in Enum.GetValues<NotificationType>())
            NotificationIconRenderer.GetIconUri(type);
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
    }
}
