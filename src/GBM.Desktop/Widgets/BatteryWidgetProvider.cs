using System.Runtime.InteropServices;
using System.Text.Json;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Microsoft.Windows.Widgets.Providers;

namespace GBM.Desktop.Widgets;

/// <summary>
/// Windows 11 widget provider that displays mouse battery status in the Widgets board.
/// Registered via sparse MSIX manifest with COM class ID 7719E9DB-2949-4ABF-977A-15E9E22D5F7B.
/// </summary>
[ComVisible(true)]
[ClassInterface(ClassInterfaceType.None)]
[Guid("7719E9DB-2949-4ABF-977A-15E9E22D5F7B")]
public sealed class BatteryWidgetProvider : IWidgetProvider
{
    private readonly IBatteryMonitorService _monitor;
    private readonly ILogger<BatteryWidgetProvider> _logger;
    private string _widgetId = string.Empty;

    public BatteryWidgetProvider(IBatteryMonitorService monitor, ILogger<BatteryWidgetProvider> logger)
    {
        _monitor = monitor;
        _logger = logger;
        _monitor.BatteryStateChanged += OnBatteryStateChanged;
    }

    public void CreateWidget(WidgetContext widgetContext)
    {
        _widgetId = widgetContext.Id;
        _logger.LogInformation("[WIDGET] Widget created: {Id}", _widgetId);
        UpdateWidget();
    }

    public void DeleteWidget(string widgetId, string customState)
    {
        _logger.LogInformation("[WIDGET] Widget deleted: {Id}", widgetId);
        _widgetId = string.Empty;
    }

    public void OnActionInvoked(WidgetActionInvokedArgs actionInvokedArgs) { }

    public void OnWidgetContextChanged(WidgetContextChangedArgs contextChangedArgs)
    {
        UpdateWidget();
    }

    public void Activate(WidgetContext widgetContext)
    {
        _widgetId = widgetContext.Id;
        UpdateWidget();
    }

    public void Deactivate(string widgetId) { }

    private void OnBatteryStateChanged(BatteryState state) => UpdateWidget();

    private void UpdateWidget()
    {
        if (string.IsNullOrEmpty(_widgetId))
            return;

        try
        {
            var state = _monitor.CurrentState;
            var template = BuildAdaptiveCardTemplate();
            var data = BuildAdaptiveCardData(state);

            var updateOptions = new WidgetUpdateRequestOptions(_widgetId)
            {
                Template = template,
                Data = data,
                CustomState = state.Level.ToString()
            };

            WidgetManager.GetDefault().UpdateWidget(updateOptions);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[WIDGET] Failed to update widget");
        }
    }

    private static string BuildAdaptiveCardTemplate() =>
        """
        {
          "type": "AdaptiveCard",
          "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
          "version": "1.5",
          "body": [
            {
              "type": "TextBlock",
              "text": "${deviceName}",
              "weight": "Bolder",
              "size": "Medium"
            },
            {
              "type": "TextBlock",
              "text": "${batteryPercent}%",
              "size": "ExtraLarge",
              "weight": "Bolder",
              "color": "${batteryColor}"
            },
            {
              "type": "TextBlock",
              "text": "${chargingStatus}",
              "size": "Small",
              "isSubtle": true
            },
            {
              "type": "TextBlock",
              "text": "Updated ${lastUpdated}",
              "size": "Small",
              "isSubtle": true
            }
          ]
        }
        """;

    private static string BuildAdaptiveCardData(BatteryState state)
    {
        var color = state.Level switch
        {
            <= 20 => "Attention",
            <= 50 => "Warning",
            _ => "Good"
        };

        var chargingStatus = state.Connection switch
        {
            ConnectionState.NotConnected => "Disconnected",
            ConnectionState.Connecting => "Connecting...",
            ConnectionState.Sleeping => "Sleeping",
            _ => state.IsCharging ? "⚡ Charging" : "🔋 On battery"
        };

        return JsonSerializer.Serialize(new
        {
            deviceName = state.DeviceName,
            batteryPercent = state.Level,
            batteryColor = color,
            chargingStatus,
            lastUpdated = state.LastReadTime == DateTime.MinValue
                ? "—"
                : state.LastReadTime.ToLocalTime().ToString("HH:mm")
        });
    }
}
