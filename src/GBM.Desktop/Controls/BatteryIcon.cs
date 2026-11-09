using Avalonia;
using Avalonia.Controls;
using Avalonia.Media;

namespace GBM.Desktop.Controls;

public sealed class BatteryIcon : Control
{
    public static readonly StyledProperty<int> LevelProperty =
        AvaloniaProperty.Register<BatteryIcon, int>(nameof(Level));

    public static readonly StyledProperty<bool> IsChargingProperty =
        AvaloniaProperty.Register<BatteryIcon, bool>(nameof(IsCharging));

    public static readonly StyledProperty<bool> IsConnectedProperty =
        AvaloniaProperty.Register<BatteryIcon, bool>(nameof(IsConnected));

    public int Level
    {
        get => GetValue(LevelProperty);
        set => SetValue(LevelProperty, value);
    }

    public bool IsCharging
    {
        get => GetValue(IsChargingProperty);
        set => SetValue(IsChargingProperty, value);
    }

    public bool IsConnected
    {
        get => GetValue(IsConnectedProperty);
        set => SetValue(IsConnectedProperty, value);
    }

    protected override void OnPropertyChanged(AvaloniaPropertyChangedEventArgs change)
    {
        base.OnPropertyChanged(change);

        if (change.Property == LevelProperty ||
            change.Property == IsChargingProperty ||
            change.Property == IsConnectedProperty)
        {
            InvalidateVisual();
        }
    }

    public override void Render(DrawingContext context)
    {
        base.Render(context);

        double width = Bounds.Width;
        double height = Bounds.Height;
        if (width < 10 || height < 14)
            return;

        Color color = GetLevelColor();
        var brush = new SolidColorBrush(color);
        var body = new Rect(1.5, 4, width - 3, height - 5);
        var cap = new Rect(width / 2 - 2.5, 1, 5, 2.5);
        var inner = new Rect(body.X + 2, body.Y + 2, body.Width - 4, body.Height - 4);

        // Three horizontal cells match the classic battery-gauge silhouette.
        const int segmentCount = 3;
        const double segmentGap = 0.7;
        double segmentHeight = (inner.Height - segmentGap * (segmentCount - 1)) / segmentCount;
        int filledSegments = IsConnected
            ? (int)Math.Ceiling(Math.Clamp(Level, 0, 100) / 100.0 * segmentCount)
            : 0;

        for (int index = 0; index < segmentCount; index++)
        {
            int segmentFromBottom = segmentCount - index - 1;
            double y = inner.Y + index * (segmentHeight + segmentGap);
            var segment = new Rect(inner.X, y, inner.Width, segmentHeight);

            if (segmentFromBottom < filledSegments)
            {
                context.FillRectangle(brush, segment, 0.6f);
            }
            else
            {
                var mutedColor = Color.FromArgb(55, color.R, color.G, color.B);
                context.FillRectangle(new SolidColorBrush(mutedColor), segment, 0.6f);
            }
        }

        context.DrawRectangle(new Pen(brush, 1.4), body, 1.2f);
        context.FillRectangle(brush, cap, 0.5f);
    }

    private Color GetLevelColor()
    {
        if (!IsConnected)
            return Color.Parse("#6b7280");
        if (IsCharging)
            return Color.Parse("#8b5cf6");
        if (Level >= 50)
            return Color.Parse("#22c55e");
        if (Level >= 20)
            return Color.Parse("#f59e0b");
        return Color.Parse("#ef4444");
    }
}
