using GBM.Core.Models;
using SkiaSharp;

namespace GBM.Desktop.Services;

/// <summary>
/// Renders circular PNG notification icons for each battery alert type using SkiaSharp.
/// Icons are written to %TEMP%\GBM\icons\ on first use and reused thereafter.
/// Uses SkiaSharp (already loaded by Avalonia) — zero additional native memory cost.
/// </summary>
public static class NotificationIconRenderer
{
    private static readonly string IconDir = Path.Combine(
        Path.GetTempPath(), "GBM", "icons");

    // Color palette matches the app's dark theme accent colors
    private static readonly SKColor CriticalRed    = new(0xDC, 0x26, 0x26);
    private static readonly SKColor LowAmber       = new(0xD9, 0x77, 0x06);
    private static readonly SKColor FullGreen      = new(0x05, 0x96, 0x69);
    private static readonly SKColor DisconnectGray = new(0x6B, 0x72, 0x80);

    /// <summary>
    /// Returns a file:/// URI to a cached PNG icon for the given notification type.
    /// Creates the icon on first call. Returns null if rendering fails.
    /// </summary>
    public static string? GetIconUri(NotificationType type)
    {
        try
        {
            Directory.CreateDirectory(IconDir);
            string fileName = $"notif-{type.ToString().ToLowerInvariant()}.png";
            string filePath = Path.Combine(IconDir, fileName);

            if (!File.Exists(filePath))
                RenderIcon(type, filePath);

            string uri = "file:///" + filePath.Replace('\\', '/');
            return uri;
        }
        catch
        {
            return null;
        }
    }

    private static void RenderIcon(NotificationType type, string outputPath)
    {
        const int size = 96;

        SKColor bg = type switch
        {
            NotificationType.Critical     => CriticalRed,
            NotificationType.Low          => LowAmber,
            NotificationType.FullCharge   => FullGreen,
            NotificationType.Disconnected => DisconnectGray,
            _                             => DisconnectGray
        };

        var imageInfo = new SKImageInfo(size, size, SKColorType.Rgba8888, SKAlphaType.Premul);

        using var surface = SKSurface.Create(imageInfo);
        var canvas = surface.Canvas;
        canvas.Clear(SKColors.Transparent);

        // Background circle
        using (var bgPaint = new SKPaint
        {
            Color = bg,
            IsAntialias = true,
            Style = SKPaintStyle.Fill
        })
        {
            canvas.DrawCircle(size / 2f, size / 2f, size / 2f - 0.5f, bgPaint);
        }

        // Draw content
        if (type == NotificationType.Disconnected)
            DrawPlugIcon(canvas, size);
        else
            DrawBatteryIcon(canvas, size, type);

        // Save as PNG
        using var image = surface.Snapshot();
        using var data = image.Encode(SKEncodedImageFormat.Png, 100);
        using var stream = File.OpenWrite(outputPath);
        data.SaveTo(stream);
    }

    private static void DrawBatteryIcon(SKCanvas canvas, int size, NotificationType type)
    {
        float margin = size * 0.20f;
        float termW  = size * 0.06f;
        float termH  = size * 0.18f;
        float bodyX  = margin;
        float bodyY  = size * 0.30f;
        float bodyW  = size - margin * 2 - termW;
        float bodyH  = size * 0.40f;
        float rx     = size * 0.06f;
        float strokeW = size * 0.055f;

        using var whiteFill = new SKPaint
        {
            Color = SKColors.White,
            IsAntialias = true,
            Style = SKPaintStyle.Fill
        };
        using var whiteStroke = new SKPaint
        {
            Color = SKColors.White,
            IsAntialias = true,
            Style = SKPaintStyle.Stroke,
            StrokeWidth = strokeW,
            StrokeJoin = SKStrokeJoin.Round
        };
        using var dimFill = new SKPaint
        {
            Color = new SKColor(255, 255, 255, 80),
            IsAntialias = true,
            Style = SKPaintStyle.Fill
        };

        // Terminal nub
        float termX = bodyX + bodyW;
        float termY = bodyY + (bodyH - termH) / 2f;
        canvas.DrawRoundRect(termX, termY, termW, termH, rx * 0.5f, rx * 0.5f, whiteFill);

        // Battery outline
        canvas.DrawRoundRect(bodyX, bodyY, bodyW, bodyH, rx, rx, whiteStroke);

        // Fill track
        float fillPad  = size * 0.055f;
        float fillMaxW = bodyW - fillPad * 2;
        float fillH    = bodyH - fillPad * 2;
        float fillX    = bodyX + fillPad;
        float fillY    = bodyY + fillPad;
        float fillRx   = rx * 0.4f;

        canvas.DrawRoundRect(fillX, fillY, fillMaxW, fillH, fillRx, fillRx, dimFill);

        // Fill bar
        float fillFraction = type switch
        {
            NotificationType.Critical   => 0.10f,
            NotificationType.Low        => 0.30f,
            NotificationType.FullCharge => 1.00f,
            _                           => 0.50f
        };

        float fillW = fillMaxW * fillFraction;
        if (fillW > fillRx)
            canvas.DrawRoundRect(fillX, fillY, fillW, fillH, fillRx, fillRx, whiteFill);

        // Bolt for FullCharge
        if (type == NotificationType.FullCharge)
            DrawBolt(canvas, size, bodyX, bodyY, bodyW, bodyH);
    }

    private static void DrawBolt(SKCanvas canvas, int size,
        float bodyX, float bodyY, float bodyW, float bodyH)
    {
        float cx = bodyX + bodyW * 0.48f;
        float cy = bodyY + bodyH * 0.50f;
        float s  = size * 0.14f;

        var bolt = new SKPath();
        bolt.MoveTo(cx + s * 0.2f,  cy - s);
        bolt.LineTo(cx - s * 0.1f,  cy + s * 0.1f);
        bolt.LineTo(cx + s * 0.3f,  cy + s * 0.1f);
        bolt.LineTo(cx - s * 0.2f,  cy + s);
        bolt.LineTo(cx + s * 0.0f,  cy - s * 0.1f);
        bolt.LineTo(cx - s * 0.1f,  cy - s * 0.1f);
        bolt.Close();

        using var boltPaint = new SKPaint
        {
            Color = new SKColor(20, 20, 20, 200),
            IsAntialias = true,
            Style = SKPaintStyle.Fill
        };
        canvas.DrawPath(bolt, boltPaint);
    }

    private static void DrawPlugIcon(SKCanvas canvas, int size)
    {
        float cx = size * 0.50f;
        float cy = size * 0.50f;
        float r  = size * 0.22f;
        float strokeW = size * 0.07f;

        using var whitePaint = new SKPaint
        {
            Color = SKColors.White,
            IsAntialias = true,
            Style = SKPaintStyle.Stroke,
            StrokeWidth = strokeW,
            StrokeCap = SKStrokeCap.Round
        };
        using var whiteFill = new SKPaint
        {
            Color = SKColors.White,
            IsAntialias = true,
            Style = SKPaintStyle.Fill
        };

        // Circle outline
        canvas.DrawCircle(cx, cy, r, whitePaint);

        // Two prongs
        float prongW       = size * 0.07f;
        float prongH       = size * 0.15f;
        float prongSpacing = size * 0.12f;

        canvas.DrawRect(cx - prongSpacing - prongW / 2, cy - r - prongH, prongW, prongH, whiteFill);
        canvas.DrawRect(cx + prongSpacing - prongW / 2, cy - r - prongH, prongW, prongH, whiteFill);

        // Cord
        canvas.DrawLine(cx, cy + r, cx, cy + r + size * 0.12f, whitePaint);

        // Slash
        using var slashPaint = new SKPaint
        {
            Color = new SKColor(255, 255, 255, 220),
            IsAntialias = true,
            Style = SKPaintStyle.Stroke,
            StrokeWidth = size * 0.065f,
            StrokeCap = SKStrokeCap.Round
        };
        canvas.DrawLine(
            cx - size * 0.28f, cy - size * 0.28f,
            cx + size * 0.28f, cy + size * 0.28f,
            slashPaint);
    }
}
