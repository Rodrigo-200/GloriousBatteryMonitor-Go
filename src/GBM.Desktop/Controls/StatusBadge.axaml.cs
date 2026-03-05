using Avalonia;
using Avalonia.Controls;
using Avalonia.Controls.Shapes;
using Avalonia.Media;
using GBM.Core.Models;

namespace GBM.Desktop.Controls;

public partial class StatusBadge : UserControl
{
    public static readonly StyledProperty<ConnectionState> ConnectionProperty =
        AvaloniaProperty.Register<StatusBadge, ConnectionState>(nameof(Connection));

    public static readonly StyledProperty<bool> IsChargingProperty =
        AvaloniaProperty.Register<StatusBadge, bool>(nameof(IsCharging));

    public ConnectionState Connection
    {
        get => GetValue(ConnectionProperty);
        set => SetValue(ConnectionProperty, value);
    }

    public bool IsCharging
    {
        get => GetValue(IsChargingProperty);
        set => SetValue(IsChargingProperty, value);
    }

    public StatusBadge()
    {
        InitializeComponent();
    }

    protected override void OnPropertyChanged(AvaloniaPropertyChangedEventArgs change)
    {
        base.OnPropertyChanged(change);
        if (change.Property == ConnectionProperty || change.Property == IsChargingProperty)
        {
            UpdateBadge();
        }
    }

    private void UpdateBadge()
    {
        var text = this.FindControl<TextBlock>("BadgeText");
        var dot = this.FindControl<Ellipse>("StatusDot");
        var icon = this.FindControl<PathIcon>("StatusIcon");
        if (text == null) return;

        string colorKey, label;
        bool showBolt = false;

        if (IsCharging && Connection == ConnectionState.Connected)
        {
            colorKey = "BadgeChargingText";
            label = "Charging";
            showBolt = true;
        }
        else
        {
            switch (Connection)
            {
                case ConnectionState.Connected:
                    colorKey = "BadgeConnectedText";
                    label = "Connected";
                    break;
                case ConnectionState.Connecting:
                    colorKey = "BadgeConnectingText";
                    label = "Connecting";
                    break;
                case ConnectionState.LastKnown:
                    colorKey = "BadgeLastKnownText";
                    label = "Last Known";
                    break;
                case ConnectionState.Sleeping:
                    colorKey = "BadgeSleepingText";
                    label = "Sleeping";
                    break;
                default:
                    colorKey = "BadgeDisconnectedText";
                    label = "Disconnected";
                    break;
            }
        }

        // Apply color from theme resources
        if (Application.Current != null &&
            Application.Current.Resources.TryGetResource(colorKey, ActualThemeVariant, out var res) &&
            res is Color color)
        {
            var brush = new SolidColorBrush(color);
            text.Foreground = brush;
            if (dot != null) dot.Fill = brush;
            if (icon != null) icon.Foreground = brush;
        }

        // Toggle dot vs bolt icon
        if (dot != null) dot.IsVisible = !showBolt;
        if (icon != null) icon.IsVisible = showBolt;

        text.Text = label;
    }

    protected override void OnAttachedToVisualTree(VisualTreeAttachmentEventArgs e)
    {
        base.OnAttachedToVisualTree(e);
        UpdateBadge();
    }
}
