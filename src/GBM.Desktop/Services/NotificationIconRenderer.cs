using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using GBM.Core.Models;
using Microsoft.Extensions.Logging;

namespace GBM.Desktop.Services;

/// <summary>
/// Renders circular PNG notification icons for each battery alert type.
/// Icons are written to %TEMP%\GBM\icons\ on first use and reused thereafter.
/// </summary>
public static class NotificationIconRenderer
{
    private static readonly string IconDir = Path.Combine(
        Path.GetTempPath(), "GBM", "icons");

    // Color palette matches the app's dark theme accent colors
    private static readonly Color CriticalRed   = Color.FromArgb(0xDC, 0x26, 0x26);
    private static readonly Color LowAmber      = Color.FromArgb(0xD9, 0x77, 0x06);
    private static readonly Color FullGreen     = Color.FromArgb(0x05, 0x96, 0x69);
    private static readonly Color DisconnectGray = Color.FromArgb(0x6B, 0x72, 0x80);

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

            // Toast XML requires forward slashes and triple-slash for file URIs
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
        const int size = 96; // 96px — crisp at 100% and 200% DPI scaling

        Color bg = type switch
        {
            NotificationType.Critical     => CriticalRed,
            NotificationType.Low          => LowAmber,
            NotificationType.FullCharge   => FullGreen,
            NotificationType.Disconnected => DisconnectGray,
            _                             => DisconnectGray
        };

        using var bmp = new Bitmap(size, size, PixelFormat.Format32bppArgb);
        using var g = Graphics.FromImage(bmp);

        g.SmoothingMode = SmoothingMode.AntiAlias;
        g.Clear(Color.Transparent);

        // ── Background circle ──
        using (var bgBrush = new SolidBrush(bg))
            g.FillEllipse(bgBrush, 0, 0, size - 1, size - 1);

        // ── Draw content based on type ──
        if (type == NotificationType.Disconnected)
            DrawPlugIcon(g, size);
        else
            DrawBatteryIcon(g, size, type);

        bmp.Save(outputPath, ImageFormat.Png);
    }

    /// <summary>
    /// Draws a battery icon. Fill level and optional bolt vary by type.
    /// </summary>
    private static void DrawBatteryIcon(Graphics g, int size, NotificationType type)
    {
        // Battery body: horizontally centered, slightly wide
        float margin = size * 0.20f;
        float termW = size * 0.06f;
        float termH = size * 0.18f;
        float bodyX = margin;
        float bodyY = size * 0.30f;
        float bodyW = size - margin * 2 - termW;
        float bodyH = size * 0.40f;
        float rx = size * 0.06f;

        using var whitePen = new Pen(Color.White, size * 0.055f)
        {
            LineJoin = LineJoin.Round
        };
        using var whiteBrush = new SolidBrush(Color.White);
        using var dimBrush = new SolidBrush(Color.FromArgb(80, 255, 255, 255));

        // Terminal nub (right side)
        float termX = bodyX + bodyW;
        float termY = bodyY + (bodyH - termH) / 2f;
        using (var termPath = RoundedRect(termX, termY, termW, termH, rx * 0.5f))
            g.FillPath(whiteBrush, termPath);

        // Battery outline
        using (var bodyPath = RoundedRect(bodyX, bodyY, bodyW, bodyH, rx))
            g.DrawPath(whitePen, bodyPath);

        // Fill interior
        float fillPad = size * 0.055f;
        float fillMaxW = bodyW - fillPad * 2;
        float fillH = bodyH - fillPad * 2;
        float fillX = bodyX + fillPad;
        float fillY = bodyY + fillPad;

        float fillFraction = type switch
        {
            NotificationType.Critical   => 0.10f,
            NotificationType.Low        => 0.30f,
            NotificationType.FullCharge => 1.00f,
            _                           => 0.50f
        };

        // Ghost fill track (shows the unfilled portion)
        using (var trackPath = RoundedRect(fillX, fillY, fillMaxW, fillH, rx * 0.4f))
            g.FillPath(dimBrush, trackPath);

        // Solid fill bar
        float fillW = fillMaxW * fillFraction;
        if (fillW > rx)
        {
            using (var fillPath = RoundedRect(fillX, fillY, fillW, fillH, rx * 0.4f))
                g.FillPath(whiteBrush, fillPath);
        }

        // Charging bolt overlay for FullCharge
        if (type == NotificationType.FullCharge)
            DrawBolt(g, size, bodyX, bodyY, bodyW, bodyH);
    }

    /// <summary>
    /// Draws a lightning bolt centered in the battery body area.
    /// </summary>
    private static void DrawBolt(Graphics g, int size,
        float bodyX, float bodyY, float bodyW, float bodyH)
    {
        float cx = bodyX + bodyW * 0.48f;
        float cy = bodyY + bodyH * 0.50f;
        float s = size * 0.14f;

        PointF[] bolt =
        {
            new(cx + s * 0.2f, cy - s),        // top right
            new(cx - s * 0.1f, cy + s * 0.1f), // middle left
            new(cx + s * 0.3f, cy + s * 0.1f), // middle right
            new(cx - s * 0.2f, cy + s),         // bottom left
            new(cx + s * 0.0f, cy - s * 0.1f), // middle right upper
            new(cx - s * 0.1f, cy - s * 0.1f), // middle left upper
        };

        // Draw bolt in the fill color of the battery (dark bg) so it stands out on white fill
        Color boltColor = Color.FromArgb(200, 20, 20, 20);
        using var boltBrush = new SolidBrush(boltColor);
        g.FillPolygon(boltBrush, bolt);
    }

    /// <summary>
    /// Draws a plug/disconnect icon for the Disconnected notification type.
    /// </summary>
    private static void DrawPlugIcon(Graphics g, int size)
    {
        using var whitePen = new Pen(Color.White, size * 0.07f)
        {
            StartCap = LineCap.Round,
            EndCap = LineCap.Round
        };
        using var whiteBrush = new SolidBrush(Color.White);

        float cx = size * 0.50f;
        float cy = size * 0.50f;
        float r = size * 0.22f;

        // Circle outline
        g.DrawEllipse(whitePen, cx - r, cy - r, r * 2, r * 2);

        // Two prongs at top
        float prongW = size * 0.07f;
        float prongH = size * 0.15f;
        float prongSpacing = size * 0.12f;

        using var prongBrush = new SolidBrush(Color.White);
        g.FillRectangle(prongBrush,
            cx - prongSpacing - prongW / 2, cy - r - prongH,
            prongW, prongH);
        g.FillRectangle(prongBrush,
            cx + prongSpacing - prongW / 2, cy - r - prongH,
            prongW, prongH);

        // Cord line at bottom
        g.DrawLine(whitePen, cx, cy + r, cx, cy + r + size * 0.12f);

        // Diagonal slash (the "disconnected" indicator)
        using var slashPen = new Pen(Color.FromArgb(220, 255, 255, 255), size * 0.065f)
        {
            StartCap = LineCap.Round,
            EndCap = LineCap.Round
        };
        g.DrawLine(slashPen,
            cx - size * 0.28f, cy - size * 0.28f,
            cx + size * 0.28f, cy + size * 0.28f);
    }

    /// <summary>Creates a GraphicsPath for a rounded rectangle.</summary>
    private static GraphicsPath RoundedRect(float x, float y, float w, float h, float r)
    {
        float d = r * 2;
        var path = new GraphicsPath();
        path.AddArc(x, y, d, d, 180, 90);
        path.AddArc(x + w - d, y, d, d, 270, 90);
        path.AddArc(x + w - d, y + h - d, d, d, 0, 90);
        path.AddArc(x, y + h - d, d, d, 90, 90);
        path.CloseFigure();
        return path;
    }
}
