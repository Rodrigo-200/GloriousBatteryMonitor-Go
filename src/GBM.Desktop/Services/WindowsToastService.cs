using GBM.Core.Models;
using Microsoft.Extensions.Logging;
using System.Runtime.InteropServices;

namespace GBM.Desktop.Services;

/// <summary>
/// Delivers battery alerts as Windows 11 Action Center toasts
/// with rendered circular icons, contextual copy, and appropriate audio.
/// Uses native WinRT APIs via managed code.
/// </summary>
public class WindowsToastService : IDisposable
{
    private readonly ILogger<WindowsToastService> _logger;
    private bool _disposed;
    private Windows.UI.Notifications.ToastNotifier? _toastNotifier;
    private const string AppId = "GloriousBatteryMonitor.App";

    public WindowsToastService(ILogger<WindowsToastService> logger)
    {
        _logger = logger;
        RegisterAppId(); // Must run before first Show() call — synchronous

        // Initialize ToastNotifier once and reuse it
        try
        {
            _toastNotifier = Windows.UI.Notifications.ToastNotificationManager
                .CreateToastNotifier(AppId);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[TOAST] Failed to create ToastNotifier — notifications may not work");
        }

        PreRenderIcons(); // Pre-render all icons synchronously before notifications can fire
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

            // Display via direct WinRT API call
            _ = Task.Run(() => ShowToastViaWinRT(title, bodyText, iconUri, audioSrc, type));

            _logger.LogInformation("[TOAST] Delivered [{Type}]: {Title}", type, title);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex,
                "[TOAST] Failed to show notification [{Type}]: {Title}", type, title);
        }
    }

    private void ShowToastViaWinRT(string title, string message, string? iconUri, string audioSrc, NotificationType type)
    {
        try
        {
            if (_toastNotifier == null)
            {
                _logger.LogWarning("[TOAST] ToastNotifier not available");
                return;
            }

            // Build the XML template with icon if available
            string logoXml = iconUri != null
                ? $@"<image placement=""appLogoOverride"" hint-crop=""circle"" src=""{EscapeXml(iconUri)}""/>"
                : string.Empty;

            string toastXml = $@"<toast duration=""short""><visual><binding template=""ToastGeneric"">{logoXml}<text hint-maxLines=""1"">{EscapeXml(title)}</text><text>{EscapeXml(message)}</text><text placement=""attribution"">Glorious Battery Monitor</text></binding></visual><audio src=""{audioSrc}"" loop=""false""/></toast>";

            // Use direct WinRT API with proper COM object cleanup
            var doc = new Windows.Data.Xml.Dom.XmlDocument();
            try
            {
                doc.LoadXml(toastXml);
                var toast = new Windows.UI.Notifications.ToastNotification(doc);
                try
                {
                    toast.Tag = "battery";
                    toast.Group = "battery";
                    _toastNotifier.Show(toast);
                }
                finally
                {
                    Marshal.ReleaseComObject(toast);
                }
            }
            finally
            {
                Marshal.ReleaseComObject(doc);
            }

            _logger.LogDebug("[TOAST] WinRT toast displayed successfully");
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[TOAST] Exception displaying toast for type {Type}", type);
        }
    }

    /// <summary>
    /// Registers the AppId in the registry so Windows delivers toast notifications
    /// for this non-packaged app. Required before CreateToastNotifier(AppId).Show().
    /// Registry path: HKCU\SOFTWARE\Classes\AppUserModelId\{AppId}
    /// </summary>
    private void RegisterAppId()
    {
        try
        {
            string keyPath = $@"SOFTWARE\Classes\AppUserModelId\{AppId}";

            using var key = Microsoft.Win32.Registry.CurrentUser.CreateSubKey(keyPath, writable: true);
            if (key == null)
            {
                _logger.LogWarning("[TOAST] Failed to create AppId registry key — notifications may not appear");
                return;
            }

            key.SetValue("DisplayName", "Glorious Battery Monitor", Microsoft.Win32.RegistryValueKind.String);

            // Set the icon to the running executable so Windows shows the correct icon
            string exePath = System.Diagnostics.Process.GetCurrentProcess().MainModule?.FileName
                             ?? System.AppContext.BaseDirectory + "GBM.Desktop.exe";
            key.SetValue("IconUri", exePath, Microsoft.Win32.RegistryValueKind.String);

            _logger.LogInformation("[TOAST] AppId registered in registry: {Key}", keyPath);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[TOAST] Failed to register AppId in registry");
        }
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

        if (_toastNotifier != null)
        {
            try { Marshal.ReleaseComObject(_toastNotifier); }
            catch { }
            _toastNotifier = null;
        }
    }
}
