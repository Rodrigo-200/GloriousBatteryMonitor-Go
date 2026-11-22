using GBM.Desktop.Services;
using Xunit;

namespace GBM.Tests.Services;

public class MouseDpiMonitorTests
{
    [Theory]
    [InlineData("0401020640032000", 1, 1600, 800)]
    [InlineData("0501010320032000", 0, 800, 800)]
    [InlineData("0701010F07000000", 1, 1600, 800)]
    public void ParseReport_RecognizesSupportedFormats(string packet, int stage, int x, int y)
    {
        Assert.Equal(new DpiReading(stage, x, y), MouseDpiMonitorService.ParseReport(Convert.FromHexString(packet)));
    }

    [Theory]
    [InlineData("0101020640032000")]
    [InlineData("0400020640032000")]
    [InlineData("0401000640032000")]
    [InlineData("0401020000032000")]
    [InlineData("04010206400320")]
    public void ParseReport_IgnoresInvalidOrUnrelatedInput(string packet)
    {
        Assert.Null(MouseDpiMonitorService.ParseReport(Convert.FromHexString(packet)));
    }
}
