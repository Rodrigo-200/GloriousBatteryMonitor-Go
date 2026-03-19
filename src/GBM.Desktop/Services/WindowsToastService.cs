using GBM.Core.Models;
using Microsoft.Extensions.Logging;

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

    public WindowsToastService(ILogger<WindowsToastService> logger)
    {
        _logger = logger;
        // Initialize: Pre-render icons
        Task.Run(async () =>
        {
            await Task.Delay(100); // Let app finish startup
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
            // Build the XML template with icon if available
            string logoXml = iconUri != null
                ? $@"<image placement=""appLogoOverride"" hint-crop=""circle"" src=""{EscapeXml(iconUri)}""/>"
                : string.Empty;

            string toastXml = $@"<toast duration=""short""><visual><binding template=""ToastGeneric"">{logoXml}<text hint-maxLines=""1"">{EscapeXml(title)}</text><text>{EscapeXml(message)}</text><text placement=""attribution"">Glorious Battery Monitor</text></binding></visual><audio src=""{audioSrc}"" loop=""false""/></toast>";

            // Use direct WinRT API
            var doc = new Windows.Data.Xml.Dom.XmlDocument();
            doc.LoadXml(toastXml);
            var toast = new Windows.UI.Notifications.ToastNotification(doc);
            toast.Tag = "battery";
            toast.Group = "battery";
            Windows.UI.Notifications.ToastNotificationManager
                .CreateToastNotifier("GloriousBatteryMonitor.App")
                .Show(toast);

            _logger.LogDebug("[TOAST] WinRT toast displayed successfully");
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[TOAST] Exception displaying toast for type {Type}", type);
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
    }
}
