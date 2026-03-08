using System.Runtime.InteropServices;
using System.Text.Json;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Windows.Widgets.Providers;

namespace GBM.Desktop.Widgets;

/// <summary>
/// Windows 11 widget provider that displays mouse battery status in the Widgets board.
/// Registered via sparse MSIX manifest with COM class ID 7719E9DB-2949-4ABF-977A-15E9E22D5F7B.
///
/// The parameterless constructor is required for COM activation — Windows calls
/// CoCreateInstance with our CLSID, which invokes the class factory's CreateInstance,
/// which calls new BatteryWidgetProvider(). Services are resolved from App.Services.
/// </summary>
[ComVisible(true)]
[ClassInterface(ClassInterfaceType.None)]
[Guid("7719E9DB-2949-4ABF-977A-15E9E22D5F7B")]
public sealed class BatteryWidgetProvider : IWidgetProvider
{
    private readonly IBatteryMonitorService? _monitor;
    private readonly ILogger<BatteryWidgetProvider> _logger;
    private string _widgetId = string.Empty;

    /// <summary>
    /// Parameterless constructor for COM activation via CoCreateInstance.
    /// Resolves services from the static App.Services container.
    /// </summary>
    public BatteryWidgetProvider()
    {
        _monitor = App.Services?.GetService<IBatteryMonitorService>();
        _logger = App.Services?.GetService<ILogger<BatteryWidgetProvider>>()
                  ?? NullLogger<BatteryWidgetProvider>.Instance;

        if (_monitor != null)
            _monitor.BatteryStateChanged += OnBatteryStateChanged;
        else
            _logger.LogWarning("[WIDGET] COM-activated but IBatteryMonitorService not available");
    }

    public void CreateWidget(WidgetContext widgetContext)
    {
        _widgetId = widgetContext.Id;
        _logger.LogInformation("[WIDGET] Widget created: {Id}", _widgetId);

        try
        {
            UpdateWidget();
            _logger.LogInformation("[WIDGET] Initial update sent for {Id}", _widgetId);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[WIDGET] Initial update failed for {Id}", _widgetId);
        }
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
        if (string.IsNullOrEmpty(_widgetId) || _monitor == null)
            return;

        try
        {
            var state = _monitor.CurrentState;
            var card = BuildAdaptiveCard(state);

            var updateOptions = new WidgetUpdateRequestOptions(_widgetId)
            {
                Template = card,
                CustomState = state.Level.ToString()
            };

            WidgetManager.GetDefault().UpdateWidget(updateOptions);
            _logger.LogDebug("[WIDGET] Pushed update: {Level}%", state.Level);
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[WIDGET] Failed to update widget");
        }
    }

    private static string BuildAdaptiveCard(BatteryState state)
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
            _ => state.IsCharging ? "Charging" : "On battery"
        };

        var lastUpdated = state.LastReadTime == DateTime.MinValue
            ? "--:--"
            : state.LastReadTime.ToLocalTime().ToString("HH:mm");

        var deviceName = JsonEncodedText.Encode(state.DeviceName ?? "Unknown").ToString();

        return $$"""
        {
          "type": "AdaptiveCard",
          "version": "1.5",
          "body": [
            {
              "type": "TextBlock",
              "text": "{{deviceName}}",
              "weight": "Bolder",
              "size": "Medium"
            },
            {
              "type": "TextBlock",
              "text": "{{state.Level}}%",
              "size": "ExtraLarge",
              "weight": "Bolder",
              "color": "{{color}}"
            },
            {
              "type": "TextBlock",
              "text": "{{chargingStatus}}",
              "size": "Small",
              "isSubtle": true
            },
            {
              "type": "TextBlock",
              "text": "Updated {{lastUpdated}}",
              "size": "Small",
              "isSubtle": true
            }
          ]
        }
        """;
    }
}
