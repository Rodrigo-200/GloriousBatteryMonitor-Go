using System.Globalization;
using Avalonia.Data.Converters;

namespace GBM.Desktop.Converters;

public class BoolToVisibilityConverter : IValueConverter
{
    public static readonly BoolToVisibilityConverter Instance = new();
    public static readonly BoolToVisibilityConverter Inverse = new() { IsInverse = true };

    public bool IsInverse { get; set; }

    public object Convert(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        if (value is bool b)
            return IsInverse ? !b : b;
        return false;
    }

    public object ConvertBack(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        if (value is bool b)
            return IsInverse ? !b : b;
        return false;
    }
}
