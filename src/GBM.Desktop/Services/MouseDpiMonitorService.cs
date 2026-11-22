using System.Threading.Channels;
using HidSharp;
using Microsoft.Extensions.Logging;

namespace GBM.Desktop.Services;

internal readonly record struct DpiReading(int Stage, int X, int Y);

public sealed class MouseDpiMonitorService : IDisposable
{
    internal const int VendorId = 0x258A;
    internal const int ReceiverProductId = 0x2027;

    private readonly CancellationTokenSource _cts = new();
    private readonly ILogger<MouseDpiMonitorService> _logger;
    private readonly Channel<bool> _deviceChanges = Channel.CreateBounded<bool>(1);
    private readonly object _readingLock = new();
    private DpiReading? _lastReading;

    internal event Action<DpiReading?>? DpiChanged;

    public MouseDpiMonitorService(ILogger<MouseDpiMonitorService> logger)
    {
        _logger = logger;
        DeviceList.Local.Changed += OnDeviceListChanged;
        var token = _cts.Token;
        _ = Task.Run(() => InputReportLoopAsync(token));
    }

    private void OnDeviceListChanged(object? sender, DeviceListChangedEventArgs e) =>
        _deviceChanges.Writer.TryWrite(true);

    private async Task InputReportLoopAsync(CancellationToken cancellationToken)
    {
        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                try
                {
                    var devices = DeviceList.Local.GetHidDevices(VendorId, ReceiverProductId).ToArray();
                    if (devices.Length == 0)
                    {
                        await _deviceChanges.Reader.ReadAsync(cancellationToken).ConfigureAwait(false);
                        continue;
                    }

                    await Task.WhenAll(devices.Select(device =>
                        Task.Run(() => ListenDevice(device, cancellationToken), cancellationToken)))
                        .ConfigureAwait(false);
                    PublishDpi(null);
                }
                catch (Exception ex) when (ex is not OperationCanceledException)
                {
                    _logger.LogDebug(ex, "DPI input scan failed; will retry");
                }

                // A present receiver can temporarily refuse a stream while its driver starts.
                await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken).ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested) { }
    }

    private void ListenDevice(HidDevice device, CancellationToken cancellationToken)
    {
        try
        {
            var reportLength = device.GetMaxInputReportLength();
            if (reportLength < 8)
                return;

            using var stream = device.Open();
            stream.ReadTimeout = 2000;
            var buffer = new byte[reportLength];
            while (!cancellationToken.IsCancellationRequested)
            {
                int bytesRead;
                try { bytesRead = stream.Read(buffer); }
                catch (TimeoutException) { continue; }

                if (bytesRead == 0)
                    return;
                if (ParseReport(buffer.AsSpan(0, bytesRead)) is { } reading)
                    PublishDpi(reading);
            }
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or InvalidOperationException)
        {
            _logger.LogDebug(ex, "Receiver DPI input collection closed");
        }
    }

    internal static DpiReading? ParseReport(ReadOnlySpan<byte> report)
    {
        if (report.Length < 8 || report[1] != 0x01)
            return null;

        int stage = report[2] & 0x0F;
        int dpiX;
        int dpiY;
        switch (report[0])
        {
            case 0x04 or 0x05 when stage is >= 1 and <= 8:
                stage--;
                dpiX = (report[3] << 8) | report[4];
                dpiY = (report[5] << 8) | report[6];
                break;
            case 0x07:
                dpiX = (report[3] + 1) * 100;
                dpiY = (report[4] + 1) * 100;
                break;
            default:
                return null;
        }

        return dpiX is >= 100 and <= 19000 && dpiY is >= 100 and <= 19000
            ? new DpiReading(stage, dpiX, dpiY)
            : null;
    }

    private void PublishDpi(DpiReading? reading)
    {
        lock (_readingLock)
        {
            if (_lastReading == reading || _cts.IsCancellationRequested)
                return;

            _lastReading = reading;
            DpiChanged?.Invoke(reading);
        }
    }

    public void Dispose()
    {
        DeviceList.Local.Changed -= OnDeviceListChanged;
        _cts.Cancel();
        _cts.Dispose();
    }
}
