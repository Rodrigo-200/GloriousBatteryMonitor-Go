using Microsoft.Extensions.Logging;
using Velopack;
using Velopack.Sources;

namespace GBM.Core.Services;

public class UpdateService : IUpdateService
{
    private readonly ILogger<UpdateService> _logger;
    private readonly ISettingsService _settingsService;
    private readonly object _currentVersionLock = new();
    private string? _cachedCurrentVersion;
    private const string RepoUrl =
        "https://github.com/Rodrigo-200/GloriousBatteryMonitor-Go";

    public UpdateService(ILogger<UpdateService> logger, ISettingsService settingsService)
    {
        _logger = logger;
        _settingsService = settingsService;
    }

    private bool UseBetaChannel => _settingsService.Current.EnableBetaUpdates;

    private static string GetChannelName(bool useBetaChannel)
    {
        return useBetaChannel ? "beta" : "stable";
    }

    private UpdateManager CreateManager(bool useBetaChannel, bool allowVersionDowngrade = false)
    {
        var source = new GithubSource(RepoUrl, null, useBetaChannel);
        var options = new UpdateOptions
        {
            ExplicitChannel = GetChannelName(useBetaChannel),
            AllowVersionDowngrade = allowVersionDowngrade
        };

        return new UpdateManager(source, options, null);
    }

    private string ResolveCurrentVersion(bool useBetaChannel)
    {
        if (!string.IsNullOrWhiteSpace(_cachedCurrentVersion))
            return _cachedCurrentVersion;

        lock (_currentVersionLock)
        {
            if (!string.IsNullOrWhiteSpace(_cachedCurrentVersion))
                return _cachedCurrentVersion;

            try
            {
                var mgr = CreateManager(useBetaChannel);
                _cachedCurrentVersion = mgr.CurrentVersion?.ToString() ?? "dev";
            }
            catch
            {
                _cachedCurrentVersion = "dev";
            }

            return _cachedCurrentVersion;
        }
    }

    private static bool IsPrereleaseVersion(string version)
    {
        if (string.IsNullOrWhiteSpace(version))
            return false;

        return version.Contains('-', StringComparison.Ordinal);
    }

    private static bool IsPendingVersionCompatibleWithChannel(string pendingVersion, bool useBetaChannel)
    {
        bool isPendingPrerelease = IsPrereleaseVersion(pendingVersion);
        return useBetaChannel ? isPendingPrerelease : !isPendingPrerelease;
    }

    public string CurrentVersion
    {
        get
        {
            return ResolveCurrentVersion(UseBetaChannel);
        }
    }

    public async Task<UpdateCheckResult?> CheckForUpdateAsync()
    {
        try
        {
            bool useBetaChannel = UseBetaChannel;
            string channel = GetChannelName(useBetaChannel);
            string currentVersion = ResolveCurrentVersion(useBetaChannel);

            // Enable downgrade checks only when user opted back to stable from a prerelease build.
            bool allowDowngrade = !useBetaChannel && IsPrereleaseVersion(currentVersion);
            var mgr = CreateManager(useBetaChannel, allowDowngrade);

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
                    "[UPDATE] Already on latest ({V}) for {Channel} channel",
                    currentVersion,
                    channel);
                return null;
            }

            var newVersion = info.TargetFullRelease.Version.ToString();
            _logger.LogInformation(
                "[UPDATE] {Channel} update available: {New} (current: {Cur})",
                channel,
                newVersion,
                currentVersion);

            return new UpdateCheckResult(newVersion, $"{RepoUrl}/releases/latest");
        }
        catch (Exception ex)
        {
            _logger.LogWarning(ex, "[UPDATE] Update check failed");
            return null;
        }
    }

    public bool IsUpdatePendingRestart()
    {
        try
        {
            bool useBetaChannel = UseBetaChannel;
            string channel = GetChannelName(useBetaChannel);
            var mgr = CreateManager(useBetaChannel);

            if (!mgr.IsInstalled)
                return false;

            var pending = mgr.UpdatePendingRestart;
            if (pending == null)
                return false;

            string pendingVersion = pending.Version?.ToString() ?? string.Empty;
            if (!IsPendingVersionCompatibleWithChannel(pendingVersion, useBetaChannel))
            {
                _logger.LogInformation(
                    "[UPDATE] Ignoring pending {Version} because selected channel is {Channel}",
                    pendingVersion,
                    channel);
                return false;
            }

            return true;
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[UPDATE] Could not determine pending update state");
            return false;
        }
    }

    public async Task<bool> DownloadUpdateAsync(
        IProgress<int>? progress = null)
    {
        try
        {
            bool useBetaChannel = UseBetaChannel;
            string channel = GetChannelName(useBetaChannel);
            string currentVersion = ResolveCurrentVersion(useBetaChannel);
            bool allowDowngrade = !useBetaChannel && IsPrereleaseVersion(currentVersion);
            var mgr = CreateManager(useBetaChannel, allowDowngrade);

            if (!mgr.IsInstalled)
            {
                _logger.LogWarning(
                    "[UPDATE] Cannot apply — not running as installed app");
                return false;
            }

            var info = await mgr.CheckForUpdatesAsync();
            if (info == null) return false;

            _logger.LogInformation(
                "[UPDATE] Downloading {Channel} package {V}...",
                channel,
                info.TargetFullRelease.Version);

            if (progress != null)
                await mgr.DownloadUpdatesAsync(info, p => progress.Report(p));
            else
                await mgr.DownloadUpdatesAsync(info);

            _logger.LogInformation(
                "[UPDATE] Download complete — update staged for next restart/apply");
            return true;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[UPDATE] Failed to download update");
            return false;
        }
    }

    public bool ApplyPendingUpdateAndRestart(string[]? restartArgs = null)
    {
        try
        {
            var mgr = CreateManager(UseBetaChannel);

            if (!mgr.IsInstalled)
            {
                _logger.LogWarning(
                    "[UPDATE] Cannot apply pending update — not running as installed app");
                return false;
            }

            var pending = mgr.UpdatePendingRestart;
            if (pending == null)
            {
                _logger.LogInformation("[UPDATE] No pending downloaded update to apply");
                return false;
            }

            _logger.LogInformation(
                "[UPDATE] Applying pending update {V} silently and restarting",
                pending.Version);

            mgr.WaitExitThenApplyUpdates(pending, silent: true, restart: true, restartArgs);
            return true;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[UPDATE] Failed to start apply/restart flow");
            return false;
        }
    }
}
