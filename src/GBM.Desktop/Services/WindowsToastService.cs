using Microsoft.Toolkit.Uwp.Notifications;
using GBM.Core.Models;
using Microsoft.Extensions.Logging;
using System.Diagnostics;

namespace GBM.Desktop.Services;

/// <summary>
/// Delivers battery alerts as proper Windows Action Center toast notifications.
/// Replaces the old in-app toast + window-pop behavior.
/// Uses PowerShell to display toasts, which works reliably on Windows 11 for non-MSIX apps.
/// </summary>
public class WindowsToastService : IDisposable
{
    private readonly ILogger<WindowsToastService> _logger;
    private bool _disposed;

    public WindowsToastService(ILogger<WindowsToastService> logger)
    {
        _logger = logger;
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

            // Map type to emoji
            string emoji = GetNotificationEmoji(type);
            string displayTitle = $"{emoji} {title}";

            // Build toast content (validates structure)
            var content = new ToastContentBuilder()
                .AddText(displayTitle)
                .AddText(message)
                .AddAttributionText("Glorious Battery Monitor")
                .GetToastContent();

            // Display using PowerShell (works reliably on Windows 11 for non-MSIX apps)
            _ = Task.Run(() => ShowToastViaPowerShell(displayTitle, message));

            _logger.LogInformation("[TOAST] Battery notification: [{Type}] {Title}: {Message}", type, title, message);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex,
                "[TOAST] Failed to create notification [{Type}]: {Title}", type, title);
        }
    }

    private void ShowToastViaPowerShell(string title, string message)
    {
        try
        {
            // Escape PowerShell-special characters
            var escapedTitle = title.Replace("'", "''");
            var escapedMessage = message.Replace("'", "''");

            // PowerShell command to show Windows toast notification
            string psCommand = $@"
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null

$APP_ID = 'GloriousBatteryMonitor.App'

$template = @""
<toast>
    <visual>
        <binding template=""ToastText02"">
            <text id=""1"">{escapedTitle}</text>
            <text id=""2"">{escapedMessage}</text>
        </binding>
    </visual>
</toast>
""@

$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($template)
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($APP_ID).Show($toast);
";

            var psi = new ProcessStartInfo
            {
                FileName = "powershell.exe",
                Arguments = $"-NoProfile -Command \"{psCommand.Replace("\"", "\\\"")}\"",
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true
            };

            using (var process = Process.Start(psi))
            {
                process?.WaitForExit(5000); // Wait max 5 seconds
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[TOAST] Failed to display toast via PowerShell");
        }
    }

    private string GetNotificationEmoji(NotificationType type)
    {
        return type switch
        {
            NotificationType.Critical    => "🔴",
            NotificationType.Low         => "🟡",
            NotificationType.FullCharge  => "✅",
            NotificationType.Disconnected => "🔌",
            _                            => "ℹ️"
        };
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;

        try
        {
            _logger.LogDebug("[TOAST] Windows toast service disposed");
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[TOAST] Error during toast service disposal");
        }
    }
}
