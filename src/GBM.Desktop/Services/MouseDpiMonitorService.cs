using HidSharp;
using Microsoft.Extensions.Logging;

namespace GBM.Desktop.Services;

/// <summary>
/// Listens to the actual vendor input collections exposed by the Model O Pro receiver.
/// This receiver uses input report IDs 0x04/0x05; the 0x07 layout belongs to other Glorious models.
/// </summary>
public sealed class MouseDpiMonitorService : IDisposable
{
    private const int VendorId = 0x258A;
    private const int ModelOProReceiverProductId = 0x2027;
    private readonly CancellationTokenSource _cts = new();
    private readonly ILogger<MouseDpiMonitorService> _logger;
    private readonly HashSet<string> _loggedInputPackets = new(StringComparer.Ordinal);
    private string _lastPublishedState = string.Empty;

    public event Action<int, int, int, string?>? DpiChanged;

    public MouseDpiMonitorService(ILogger<MouseDpiMonitorService> logger)
    {
        _logger = logger;
        _logger.LogInformation("Starting DPI monitor for Model O Pro receiver 258A:2027");
        _ = Task.Run(() => InputReportLoopAsync(_cts.Token));
    }

    private async Task InputReportLoopAsync(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                var devices = DeviceList.Local.GetHidDevices(VendorId, ModelOProReceiverProductId)
                    .Where(device => SafeLength(device.GetMaxInputReportLength) >= 8)
                    .ToArray();

                if (devices.Length == 0)
                {
                    LogOnce("no-input", "No input HID collection with 8-byte reports found for receiver 258A:2027");
                }
                else
                {
                    LogOnce("input-count", $"Found {devices.Length} input HID collection(s) for receiver 258A:2027");
                    await Task.WhenAll(devices.Select(device =>
                        Task.Run(() => ListenDevice(device, cancellationToken), cancellationToken)));
                }
            }
            catch (OperationCanceledException)
            {
                break;
            }
            catch (Exception ex)
            {
                _logger.LogWarning(ex, "DPI input scan failed; will retry");
            }

            try { await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken); }
            catch (OperationCanceledException) { break; }
        }
    }

    private void ListenDevice(HidDevice device, CancellationToken cancellationToken)
    {
        try
        {
            using var stream = device.Open();
            stream.ReadTimeout = 2000;
            _logger.LogInformation("Listening to receiver HID input collection {Path}", device.DevicePath);

            while (!cancellationToken.IsCancellationRequested)
            {
                byte[] report;
                try { report = stream.Read(); }
                catch (TimeoutException) { continue; }

                if (report.Length < 8 || report[0] is not (0x04 or 0x05 or 0x07))
                    continue;

                LogInputSample(report);

                int stage;
                int dpiX;
                int dpiY;
                if (report[0] is 0x04 or 0x05)
                {
                    // Model O Pro receiver payload: [marker=1, active stage (1-based),
                    // X DPI high, X DPI low, Y DPI high, Y DPI low, ...].
                    int stageNumber = report[2] & 0x0F;
                    dpiX = (report[3] << 8) | report[4];
                    dpiY = (report[5] << 8) | report[6];
                    if (report[1] != 0x01 || stageNumber is < 1 or > 8)
                        continue;

                    stage = stageNumber - 1;
                }
                else
                {
                    // Legacy Model O report 0x07 encodes the stage as a zero-based
                    // nibble and X/Y DPI in 100-DPI units.
                    if (report[1] != 0x01)
                        continue;

                    stage = report[2] & 0x0F;
                    dpiX = (report[3] + 1) * 100;
                    dpiY = (report[4] + 1) * 100;
                }

                if (dpiX is < 100 or > 19000 || dpiY is < 100 or > 19000)
                    continue;

                PublishDpi(stage, dpiX, dpiY);
            }
        }
        catch (OperationCanceledException) { }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or InvalidOperationException)
        {
            LogOnce($"open-error:{device.DevicePath}", $"Could not open receiver DPI input collection: {ex.Message}");
        }
    }

    private void LogInputSample(byte[] report)
    {
        var hex = Convert.ToHexString(report);
        lock (_loggedInputPackets)
        {
            // Keep a bounded set: report IDs 0x04/0x05 are vendor events, and a DPI press
            // should only add a handful of distinct packets.
            if (_loggedInputPackets.Count < 64 && _loggedInputPackets.Add(hex))
                _logger.LogInformation("Receiver DPI input report: {Bytes}", hex);
        }
    }

    private void PublishDpi(int stage, int dpiX, int dpiY)
    {
        var state = $"{stage}:{dpiX}:{dpiY}";
        if (string.Equals(state, _lastPublishedState, StringComparison.Ordinal))
            return;

        _lastPublishedState = state;
        _logger.LogInformation("Active mouse DPI report: slot {Stage}, X={DpiX}, Y={DpiY}",
            stage + 1, dpiX, dpiY);
        DpiChanged?.Invoke(stage, dpiX, dpiY, null);
    }

    private void LogOnce(string key, string message)
    {
        lock (_loggedInputPackets)
        {
            if (_loggedInputPackets.Add(key))
                _logger.LogWarning("{Message}", message);
        }
    }

    private static int SafeLength(Func<int> getLength)
    {
        try { return getLength(); }
        catch { return 0; }
    }

    public void Dispose()
    {
        _cts.Cancel();
        _cts.Dispose();
    }
}
