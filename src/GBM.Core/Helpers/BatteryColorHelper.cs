namespace GBM.Core.Helpers;

public static class BatteryColorHelper
{
    // Returns a color hex string based on battery level and state
    public static string GetColorHex(int level, bool isCharging, bool isConnected)
    {
        if (!isConnected) return "#6b7280"; // Gray
        if (isCharging) return "#8b5cf6";   // Purple
        if (level >= 50) return "#22c55e";  // Green
        if (level >= 20) return "#f59e0b";  // Amber
        return "#ef4444";                    // Red
    }

    public static string GetGaugeTrackColor(bool isDark)
    {
        return isDark ? "#27272a" : "#e2e8f0";
    }
}
