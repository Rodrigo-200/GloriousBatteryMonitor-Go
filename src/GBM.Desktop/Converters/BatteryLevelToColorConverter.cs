using System.Globalization;
using Avalonia;
using Avalonia.Data.Converters;
using Avalonia.Media;

namespace GBM.Desktop.Converters;

public class BatteryLevelToColorConverter : IMultiValueConverter
{
    public static readonly BatteryLevelToColorConverter Instance = new();

    public object? Convert(IList<object?> values, Type targetType, object? parameter, CultureInfo culture)
    {
        if (values.Count < 3 ||
            values[0] is not int level ||
            values[1] is not bool isCharging ||
            values[2] is not bool isConnected)
            return new SolidColorBrush(Color.Parse("#6b7280"));

        if (!isConnected) return new SolidColorBrush(Color.Parse("#6b7280"));
        if (isCharging) return new SolidColorBrush(Color.Parse("#8b5cf6"));
        if (level >= 50) return new SolidColorBrush(Color.Parse("#22c55e"));
        if (level >= 20) return new SolidColorBrush(Color.Parse("#f59e0b"));
        return new SolidColorBrush(Color.Parse("#ef4444"));
    }
}
