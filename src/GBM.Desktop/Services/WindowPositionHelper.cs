using Avalonia;
using Avalonia.Controls;

namespace GBM.Desktop.Services;

internal static class WindowPositionHelper
{
    public static PixelPoint? GetVisiblePosition(Window window, int? x, int? y, bool includeTaskbar = false)
    {
        if (x is null || y is null)
            return null;

        var screens = window.Screens.All;
        if (screens.Count == 0)
            return new PixelPoint(x.Value, y.Value);

        var screen = screens.FirstOrDefault(s =>
            x.Value >= (includeTaskbar ? s.Bounds.X : s.WorkingArea.X) &&
            x.Value < (includeTaskbar ? s.Bounds.Right : s.WorkingArea.Right) &&
            y.Value >= (includeTaskbar ? s.Bounds.Y : s.WorkingArea.Y) &&
            y.Value < (includeTaskbar ? s.Bounds.Bottom : s.WorkingArea.Bottom))
            ?? window.Screens.Primary
            ?? screens[0];

        var area = includeTaskbar ? screen.Bounds : screen.WorkingArea;
        var scale = screen.Scaling;
        var width = (int)Math.Ceiling(window.Bounds.Width * scale);
        var height = (int)Math.Ceiling(window.Bounds.Height * scale);
        var maxX = Math.Max(area.X, area.Right - width);
        var maxY = Math.Max(area.Y, area.Bottom - height);

        return new PixelPoint(
            Math.Clamp(x.Value, area.X, maxX),
            Math.Clamp(y.Value, area.Y, maxY));
    }
}
