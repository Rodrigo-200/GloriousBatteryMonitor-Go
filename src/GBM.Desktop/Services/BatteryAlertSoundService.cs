using Avalonia.Threading;
using Microsoft.Extensions.Logging;
using Windows.Media.Core;
using Windows.Media.Playback;
using Windows.Storage;

namespace GBM.Desktop.Services;

public sealed class BatteryAlertSoundService : IDisposable
{
    private static readonly HashSet<string> SupportedExtensions = new(StringComparer.OrdinalIgnoreCase)
    {
        ".wav", ".mp3", ".flac"
    };

    private readonly ILogger<BatteryAlertSoundService> _logger;
    private MediaPlayer? _player;

    public BatteryAlertSoundService(ILogger<BatteryAlertSoundService> logger)
    {
        _logger = logger;
    }

    public async Task<bool> PlayAsync(string? filePath)
    {
        if (string.IsNullOrWhiteSpace(filePath) || !File.Exists(filePath))
            return false;

        if (!SupportedExtensions.Contains(Path.GetExtension(filePath)))
        {
            _logger.LogWarning("Unsupported critical battery sound format: {Path}", filePath);
            return false;
        }

        try
        {
            var file = await StorageFile.GetFileFromPathAsync(Path.GetFullPath(filePath));
            var source = MediaSource.CreateFromStorageFile(file);

            await Dispatcher.UIThread.InvokeAsync(() =>
            {
                _player?.Dispose();
                _player = new MediaPlayer { Volume = 1.0 };
                _player.MediaFailed += (_, args) =>
                    _logger.LogWarning("Could not play critical battery sound {Path}: {Error}",
                        filePath, args.ErrorMessage);
                _player.Source = source;
                _player.Play();
            });

            return true;
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "Could not play critical battery sound {Path}", filePath);
            return false;
        }
    }

    public void Dispose()
    {
        if (Dispatcher.UIThread.CheckAccess())
        {
            _player?.Dispose();
            _player = null;
            return;
        }

        Dispatcher.UIThread.Post(() =>
        {
            _player?.Dispose();
            _player = null;
        });
    }
}
