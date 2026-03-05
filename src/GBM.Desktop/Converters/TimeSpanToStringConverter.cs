using System.Globalization;
using Avalonia.Data.Converters;

namespace GBM.Desktop.Converters;

public class TimeSpanToStringConverter : IValueConverter
{
    public static readonly TimeSpanToStringConverter Instance = new();

    public object Convert(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        if (value is TimeSpan ts && ts > TimeSpan.Zero)
        {
            var hours = (int)ts.TotalHours;
            var minutes = ts.Minutes;
            if (hours > 0)
                return $"{hours}h {minutes}m";
            return $"{minutes}m";
        }
        return "—";
    }

    public object ConvertBack(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        throw new NotSupportedException();
    }
}
