using Avalonia;
using Avalonia.Controls;
using Avalonia.Media;
using Avalonia.Media.Imaging;
using System;
using System.Collections.Generic;

namespace GBM.Desktop.Services;

internal static class TrayIconRenderer
{
    private const int IconSize = 32;
    private const int MaxCachedIcons = 96;
    private static readonly object CacheLock = new();
    private static readonly Dictionary<IconCacheKey, CacheEntry> IconCache = new();
    private static readonly LinkedList<IconCacheKey> CacheOrder = new();

    private static readonly Color TealColor = Color.Parse("#22c55e");
    private static readonly Color AmberColor = Color.Parse("#f59e0b");
    private static readonly Color RedColor = Color.Parse("#ef4444");
    private static readonly Color BlueColor = Color.Parse("#8b5cf6");
    private static readonly Color GrayColor = Color.Parse("#6B7280");
    private static readonly Color OutlineColor = Color.Parse("#9CA3AF");

    public static WindowIcon? RenderIcon(int level, bool isCharging, bool isConnected)
    {
        level = Math.Clamp(level, 0, 100);
        var cacheKey = NormalizeKey(level, isCharging, isConnected);

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
                    Level = cacheKey.Level,
                    IsCharging = cacheKey.IsCharging,
                    IsConnected = cacheKey.IsConnected,
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

    private static IconCacheKey NormalizeKey(int level, bool isCharging, bool isConnected)
    {
        if (!isConnected)
        {
            // Disconnected icon rendering does not vary by level/charging/percentage mode.
            return new IconCacheKey(0, false, false);
        }

        return new IconCacheKey(level, isCharging, true);
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
        bool IsConnected);

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
        public override void Render(DrawingContext context)
        {
            RenderBatteryMode(context);
        }

        private void RenderBatteryMode(DrawingContext ctx)
        {
            var fillColor = GetFillColor(Level, IsCharging, IsConnected);
            var outline = new SolidColorBrush(IsConnected ? fillColor : OutlineColor);
            var brush = new SolidColorBrush(fillColor);
            var body = new Rect(8, 5, 16, 24);
            ctx.DrawRectangle(null, new Pen(outline, 1.7), new RoundedRect(body, 2));
            ctx.DrawRectangle(brush, null, new RoundedRect(new Rect(13, 2, 6, 3), 1));

            const double innerX = 10.5, innerWidth = 11, segmentHeight = 5.2, gap = 1.1;
            var filled = IsConnected ? (int)Math.Ceiling(Math.Clamp(Level, 0, 100) / 100d * 3) : 0;
            for (var i = 0; i < 3; i++)
            {
                var y = 7.5 + i * (segmentHeight + gap);
                var opacity = (2 - i) < filled ? 1.0 : 0.22;
                ctx.DrawRectangle(new SolidColorBrush(fillColor, opacity), null,
                    new RoundedRect(new Rect(innerX, y, innerWidth, segmentHeight), 0.8));
            }
        }
    }
}
