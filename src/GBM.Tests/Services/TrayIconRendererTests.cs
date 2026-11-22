using FluentAssertions;
using GBM.Desktop.Services;
using Xunit;

namespace GBM.Tests.Services;

public class TrayIconRendererTests
{
    [Theory]
    [InlineData(true, true, true)]
    [InlineData(true, false, false)]
    [InlineData(false, true, false)]
    [InlineData(false, false, false)]
    public void ShouldRenderPercentage_RespectsConnectionAndUserSetting(
        bool isConnected,
        bool showPercentageSetting,
        bool expected)
    {
        TrayIconRenderer.ShouldRenderPercentage(isConnected, showPercentageSetting)
            .Should().Be(expected);
    }
}
