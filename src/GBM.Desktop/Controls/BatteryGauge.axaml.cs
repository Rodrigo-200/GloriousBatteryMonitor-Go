using Avalonia;
using Avalonia.Controls;
using Avalonia.Media;
using Avalonia.Threading;
using System;

namespace GBM.Desktop.Controls;

public partial class BatteryGauge : UserControl
{
    public static readonly StyledProperty<int> LevelProperty =
        AvaloniaProperty.Register<BatteryGauge, int>(nameof(Level), 0);

    public static readonly StyledProperty<bool> IsChargingProperty =
        AvaloniaProperty.Register<BatteryGauge, bool>(nameof(IsCharging), false);

    public static readonly StyledProperty<bool> IsConnectedProperty =
        AvaloniaProperty.Register<BatteryGauge, bool>(nameof(IsConnected), false);

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

    private double _displayLevel;
    private DispatcherTimer? _animTimer;

    private const double StrokeWidth = 10.0;
    private const double GlowExtra = 16.0;
    private const double ArcPadding = 18.0;
    private const double ArcStartAngle = 135.0;
    private const double ArcSweep = 270.0;

    public BatteryGauge()
    {
        InitializeComponent();
        _animTimer = new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(16) };
        _animTimer.Tick += AnimTick;
    }

    protected override void OnPropertyChanged(AvaloniaPropertyChangedEventArgs change)
    {
        base.OnPropertyChanged(change);
        if (change.Property == LevelProperty ||
            change.Property == IsChargingProperty ||
            change.Property == IsConnectedProperty)
        {
            _animTimer?.Start();
            UpdateCenterContent();
        }
    }

    private void AnimTick(object? sender, EventArgs e)
    {
        var target = (double)Level;
        var diff = target - _displayLevel;
        if (Math.Abs(diff) < 0.5)
        {
            _displayLevel = target;
            _animTimer?.Stop();
        }
        else
        {
            _displayLevel += diff * 0.10;
        }
        InvalidateVisual();
    }

    private void UpdateCenterContent()
    {
        var percentText = this.FindControl<TextBlock>("PercentText");
        var percentSuffix = this.FindControl<TextBlock>("PercentSuffix");
        var chargingBolt = this.FindControl<PathIcon>("ChargingBolt");

        if (percentText != null)
            percentText.Text = IsConnected ? $"{Level}" : "\u2014";

        if (percentSuffix != null)
            percentSuffix.IsVisible = IsConnected;

        if (chargingBolt != null)
            chargingBolt.IsVisible = IsCharging && IsConnected;
    }

    public override void Render(DrawingContext context)
    {
        base.Render(context);

        var w = Bounds.Width;
        var h = Bounds.Height;
        if (w <= 0 || h <= 0) return;

        var diameter = Math.Min(w, h);
        var cx = w / 2.0;
        var cy = h / 2.0;
        var radius = (diameter / 2.0) - StrokeWidth - ArcPadding;
        if (radius <= 0) return;

        // Draw track arc (full 270 degrees)
        var trackColor = ResolveColor("GaugeTrack", Color.Parse("#1A1A22"));
        var trackPen = new Pen(
            new SolidColorBrush(trackColor),
            StrokeWidth,
            lineCap: PenLineCap.Round);
        DrawArc(context, cx, cy, radius, ArcStartAngle, ArcSweep, trackPen);

        // Draw fill arc (proportional to battery level)
        if (IsConnected && _displayLevel > 0)
        {
            var fillSweep = ArcSweep * Math.Clamp(_displayLevel, 0, 100) / 100.0;
            if (fillSweep < 1) fillSweep = 1;

            var color = GetLevelColor();

            // Glow layer (thicker, semi-transparent)
            var glowColor = Color.FromArgb(30, color.R, color.G, color.B);
            var glowPen = new Pen(
                new SolidColorBrush(glowColor),
                StrokeWidth + GlowExtra,
                lineCap: PenLineCap.Round);
            DrawArc(context, cx, cy, radius, ArcStartAngle, fillSweep, glowPen);

            // Main fill layer
            var fillPen = new Pen(
                new SolidColorBrush(color),
                StrokeWidth,
                lineCap: PenLineCap.Round);
            DrawArc(context, cx, cy, radius, ArcStartAngle, fillSweep, fillPen);
        }
    }

    private static void DrawArc(DrawingContext ctx, double cx, double cy,
        double r, double startDeg, double sweepDeg, Pen pen)
    {
        if (sweepDeg <= 0) return;

        var startRad = startDeg * Math.PI / 180.0;
        var endRad = (startDeg + sweepDeg) * Math.PI / 180.0;

        var start = new Point(
            cx + r * Math.Cos(startRad),
            cy + r * Math.Sin(startRad));
        var end = new Point(
            cx + r * Math.Cos(endRad),
            cy + r * Math.Sin(endRad));

        var geometry = new StreamGeometry();
        using (var gc = geometry.Open())
        {
            gc.BeginFigure(start, false);
            gc.ArcTo(
                end,
                new Size(r, r),
                rotationAngle: 0,
                isLargeArc: sweepDeg > 180,
                SweepDirection.Clockwise);
        }

        ctx.DrawGeometry(null, pen, geometry);
    }

    private Color GetLevelColor()
    {
        if (!IsConnected)
            return ResolveColor("BatteryDisconnected", Color.Parse("#2A2A34"));
        if (IsCharging)
            return ResolveColor("BatteryCharging", Color.Parse("#00F0FF"));
        if (Level >= 50)
            return ResolveColor("BatteryGreen", Color.Parse("#00F0FF"));
        if (Level >= 20)
            return ResolveColor("BatteryAmber", Color.Parse("#FFAA00"));
        return ResolveColor("BatteryRed", Color.Parse("#FF3355"));
    }

    private Color ResolveColor(string key, Color fallback)
    {
        if (Application.Current != null &&
            Application.Current.Resources.TryGetResource(key, ActualThemeVariant, out var res) &&
            res is Color color)
        {
            return color;
        }
        return fallback;
    }
}
