using Avalonia.Data.Converters;
using Avalonia.Media.Transformation;
using System;
using System.Collections.Generic;
using System.Globalization;

namespace GBM.Desktop.Converters;

public sealed class ThemeSelectedIndicatorTransformConverter : IMultiValueConverter
{
    public object Convert(
        IList<object?> values,
        Type targetType,
        object? parameter,
        CultureInfo culture)
    {
        if (values.Count < 2 ||
            values[0] is not double trackWidth ||
            values[1] is not int selectedIndex ||
            trackWidth <= 0)
        {
            return TransformOperations.Parse("translateX(0px)");
        }

        selectedIndex = Math.Clamp(selectedIndex, 0, 2);
        double offset = selectedIndex * (trackWidth / 3.0);
        return TransformOperations.Parse(
            FormattableString.Invariant($"translateX({offset:0.###}px)"));
    }
}
