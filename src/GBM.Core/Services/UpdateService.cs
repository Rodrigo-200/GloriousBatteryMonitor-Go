using Microsoft.Extensions.Logging;
using Velopack;
using Velopack.Sources;

namespace GBM.Core.Services;

public class UpdateService : IUpdateService
{
    private readonly ILogger<UpdateService> _logger;
    private const string RepoUrl =
        "https://github.com/Rodrigo-200/GloriousBatteryMonitor-Go";

    public UpdateService(ILogger<UpdateService> logger)
    {
        _logger = logger;
    }

    public string CurrentVersion
    {
        get
        {
            try
            {
                var mgr = new UpdateManager(new GithubSource(RepoUrl, null, false));
                return mgr.CurrentVersion?.ToString() ?? "dev";
            }
            catch
            {
                return "dev";
            }
        }
    }

    public async Task<UpdateCheckResult?> CheckForUpdateAsync()
    {
        try
        {
            var mgr = new UpdateManager(new GithubSource(RepoUrl, null, false));

            if (!mgr.IsInstalled)
            {
                _logger.LogInformation(
                    "[UPDATE] Not running as installed app — skipping check");
                return null;
            }

            var info = await mgr.CheckForUpdatesAsync();
            if (info == null)
            {
                _logger.LogInformation(
                    "[UPDATE] Already on latest ({V})", CurrentVersion);
                return null;
            }

            var newVersion = info.TargetFullRelease.Version.ToString();
            _logger.LogInformation(
                "[UPDATE] Update available: {New} (current: {Cur})",
                newVersion, CurrentVersion);

            return new UpdateCheckResult(newVersion, $"{RepoUrl}/releases/latest");
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[UPDATE] Update check failed");
            return null;
        }
    }

    public async Task<bool> DownloadAndApplyUpdateAsync(
        IProgress<int>? progress = null)
    {
        try
        {
            var mgr = new UpdateManager(new GithubSource(RepoUrl, null, false));

            if (!mgr.IsInstalled)
            {
                _logger.LogWarning(
                    "[UPDATE] Cannot apply — not running as installed app");
                return false;
            }

            var info = await mgr.CheckForUpdatesAsync();
            if (info == null) return false;

            _logger.LogInformation(
                "[UPDATE] Downloading {V}...",
                info.TargetFullRelease.Version);

            await mgr.DownloadUpdatesAsync(info, progress != null ? p => progress.Report(p) : null);

            _logger.LogInformation(
                "[UPDATE] Download complete — restarting to apply");

            mgr.ApplyUpdatesAndRestart(info);
            return true;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[UPDATE] Failed to apply update");
            return false;
        }
    }
}
