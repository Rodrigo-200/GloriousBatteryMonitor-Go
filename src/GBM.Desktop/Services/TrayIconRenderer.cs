using Avalonia;
using Avalonia.Controls;
using Avalonia.Media;
using Avalonia.Media.Imaging;
using System;
using System.Collections.Generic;
using System.Globalization;

namespace GBM.Desktop.Services;

internal static class TrayIconRenderer
{
    private const int IconSize = 32;
    private const int MaxCachedIcons = 96;
    private static readonly object CacheLock = new();
    private static readonly Dictionary<IconCacheKey, CacheEntry> IconCache = new();
    private static readonly LinkedList<IconCacheKey> CacheOrder = new();

    private static readonly Color TealColor = Color.Parse("#00E5CC");
    private static readonly Color AmberColor = Color.Parse("#FF8A50");
    private static readonly Color RedColor = Color.Parse("#FF5252");
    private static readonly Color BlueColor = Color.Parse("#00B4FF");
    private static readonly Color GrayColor = Color.Parse("#6B7280");
    private static readonly Color OutlineColor = Color.Parse("#9CA3AF");

    public static WindowIcon? RenderIcon(int level, bool isCharging, bool isConnected, bool showPercentage)
    {
        level = Math.Clamp(level, 0, 100);
        var cacheKey = new IconCacheKey(level, isCharging, isConnected, showPercentage);

        try
        {
            lock (CacheLock)
            {
                if (IconCache.TryGetValue(cacheKey, out var cachedEntry))
                {
                    TouchCacheEntry(cachedEntry);
                    return cachedEntry.Icon;
                }

                var canvas = new IconCanvas
                {
                    Level = level,
                    IsCharging = isCharging,
                    IsConnected = isConnected,
                    ShowPercentage = showPercentage,
                    Width = IconSize,
                    Height = IconSize
                };

                canvas.Measure(new Size(IconSize, IconSize));
                canvas.Arrange(new Rect(0, 0, IconSize, IconSize));

                using var rtb = new RenderTargetBitmap(new PixelSize(IconSize, IconSize), new Vector(96, 96));
                rtb.Render(canvas);

                var icon = new WindowIcon(rtb);
                AddCacheEntry(cacheKey, icon);
                return icon;
            }
        }
        catch
        {
            return null;
        }
    }

    private static void TouchCacheEntry(CacheEntry entry)
    {
        if (entry.Node == CacheOrder.First)
            return;

        CacheOrder.Remove(entry.Node);
        CacheOrder.AddFirst(entry.Node);
    }

    private static void AddCacheEntry(IconCacheKey key, WindowIcon icon)
    {
        var node = new LinkedListNode<IconCacheKey>(key);
        CacheOrder.AddFirst(node);
        IconCache[key] = new CacheEntry(icon, node);

        if (IconCache.Count <= MaxCachedIcons)
            return;

        var lastNode = CacheOrder.Last;
        if (lastNode == null)
            return;

        CacheOrder.RemoveLast();
        IconCache.Remove(lastNode.Value);
    }

    private sealed class CacheEntry
    {
        public CacheEntry(WindowIcon icon, LinkedListNode<IconCacheKey> node)
        {
            Icon = icon;
            Node = node;
        }

        public WindowIcon Icon { get; }
        public LinkedListNode<IconCacheKey> Node { get; }
    }

    private readonly record struct IconCacheKey(
        int Level,
        bool IsCharging,
        bool IsConnected,
        bool ShowPercentage);

    private static Color GetFillColor(int level, bool isCharging, bool isConnected)
    {
        if (!isConnected) return GrayColor;
        if (isCharging) return BlueColor;
        if (level >= 50) return TealColor;
        if (level >= 20) return AmberColor;
        return RedColor;
    }

    private sealed class IconCanvas : Control
    {
        public int Level { get; init; }
        public bool IsCharging { get; init; }
        public bool IsConnected { get; init; }
        public bool ShowPercentage { get; init; }

        public override void Render(DrawingContext context)
        {
            if (ShowPercentage && IsConnected)
                RenderPercentageMode(context);
            else
                RenderBatteryMode(context);
        }

        private void RenderBatteryMode(DrawingContext ctx)
        {
            var fillColor = GetFillColor(Level, IsCharging, IsConnected);
            var outlinePen = new Pen(new SolidColorBrush(IsConnected ? fillColor : OutlineColor), 1.5);

            // Battery body outline: 22x18 centered vertically
            var bodyRect = new RoundedRect(new Rect(2, 7, 22, 18), 2);
            ctx.DrawRectangle(null, outlinePen, bodyRect);

            // Battery positive terminal on the right
            var tipBrush = new SolidColorBrush(IsConnected ? fillColor : OutlineColor);
            var tipRect = new RoundedRect(new Rect(24, 11, 4, 10), 1);
            ctx.DrawRectangle(tipBrush, null, tipRect);

            // Fill interior proportional to level
            if (IsConnected && Level > 0)
            {
                var maxFillWidth = 18.0;
                var fillWidth = Math.Max(2, Math.Round(maxFillWidth * Level / 100.0));
                var fillRect = new RoundedRect(new Rect(4, 9, fillWidth, 14), 1);
                ctx.DrawRectangle(new SolidColorBrush(fillColor), null, fillRect);
            }

            // Charging: white lightning bolt overlay
            if (IsCharging && IsConnected)
            {
                var bolt = Geometry.Parse("M14,10 L12,17 L13,17 L11,23 L16,15 L15,15 Z");
                ctx.DrawGeometry(new SolidColorBrush(Colors.White), null, bolt);
            }
        }

        private void RenderPercentageMode(DrawingContext ctx)
        {
            var fillColor = GetFillColor(Level, IsCharging, IsConnected);
            var text = Level.ToString();
            var fontSize = Level >= 100 ? 14.0 : (Level >= 10 ? 18.0 : 22.0);

            var formattedText = new FormattedText(
                text,
                CultureInfo.InvariantCulture,
                FlowDirection.LeftToRight,
                new Typeface(FontFamily.Default, FontStyle.Normal, FontWeight.Bold),
                fontSize,
                new SolidColorBrush(fillColor));

            var x = (IconSize - formattedText.Width) / 2.0;
            var y = (IconSize - formattedText.Height) / 2.0;
            ctx.DrawText(formattedText, new Point(x, y));

            // Charging indicator: small blue dot bottom-right
            if (IsCharging)
            {
                ctx.DrawRectangle(
                    new SolidColorBrush(BlueColor),
                    null,
                    new RoundedRect(new Rect(24, 24, 6, 6), 3));
            }
        }
    }
}
