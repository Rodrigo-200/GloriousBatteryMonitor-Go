using System.Net.Http;
using System.Text.Json;
using Microsoft.Extensions.Logging;
using Velopack;
using Velopack.Sources;

namespace GBM.Core.Services;

public class UpdateService : IUpdateService
{
    private readonly ILogger<UpdateService> _logger;
    private readonly ISettingsService _settingsService;
    private readonly object _currentVersionLock = new();
    private readonly object _releaseTagCacheLock = new();
    private string? _cachedCurrentVersion;
    private ChannelReleaseTagCache? _stableReleaseTagCache;
    private ChannelReleaseTagCache? _betaReleaseTagCache;

    private const string RepoUrl =
        "https://github.com/Rodrigo-200/GloriousBatteryMonitor-Go";
    private const string ReleasesApiUrl =
        "https://api.github.com/repos/Rodrigo-200/GloriousBatteryMonitor-Go/releases?per_page=100";
    private static readonly TimeSpan ReleaseTagCacheTtl = TimeSpan.FromMinutes(5);
    private static readonly HttpClient GitHubApiClient = CreateGitHubApiClient();

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

    private static HttpClient CreateGitHubApiClient()
    {
        var client = new HttpClient();
        client.DefaultRequestHeaders.UserAgent.ParseAdd("GloriousBatteryMonitor-Updater");
        return client;
    }

    private sealed record ChannelReleaseTagCache(string Tag, DateTimeOffset CachedAtUtc);

    private string? TryGetCachedReleaseTag(bool useBetaChannel)
    {
        lock (_releaseTagCacheLock)
        {
            var cache = useBetaChannel ? _betaReleaseTagCache : _stableReleaseTagCache;
            if (cache == null)
                return null;

            if (DateTimeOffset.UtcNow - cache.CachedAtUtc > ReleaseTagCacheTtl)
                return null;

            return cache.Tag;
        }
    }

    private void SetCachedReleaseTag(bool useBetaChannel, string tag)
    {
        lock (_releaseTagCacheLock)
        {
            var cache = new ChannelReleaseTagCache(tag, DateTimeOffset.UtcNow);
            if (useBetaChannel)
                _betaReleaseTagCache = cache;
            else
                _stableReleaseTagCache = cache;
        }
    }

    private async Task<string?> TryResolveLatestChannelTagAsync(bool useBetaChannel)
    {
        var cachedTag = TryGetCachedReleaseTag(useBetaChannel);
        if (!string.IsNullOrWhiteSpace(cachedTag))
            return cachedTag;

        string? fetchedTag = await TryFetchLatestChannelTagFromGitHubAsync(useBetaChannel);
        if (!string.IsNullOrWhiteSpace(fetchedTag))
            SetCachedReleaseTag(useBetaChannel, fetchedTag);

        return fetchedTag;
    }

    private async Task<string?> TryFetchLatestChannelTagFromGitHubAsync(bool useBetaChannel)
    {
        try
        {
            using var response = await GitHubApiClient.GetAsync(ReleasesApiUrl);
            if (!response.IsSuccessStatusCode)
            {
                _logger.LogWarning(
                    "[UPDATE] Release tag fetch failed for {Channel}: {StatusCode}",
                    GetChannelName(useBetaChannel),
                    (int)response.StatusCode);
                return null;
            }

            await using var stream = await response.Content.ReadAsStreamAsync();
            using var doc = await JsonDocument.ParseAsync(stream);

            foreach (var release in doc.RootElement.EnumerateArray())
            {
                if (!TryGetBooleanProperty(release, "draft", out bool isDraft) || isDraft)
                    continue;

                if (!TryGetBooleanProperty(release, "prerelease", out bool isPrerelease))
                    continue;

                if (isPrerelease != useBetaChannel)
                    continue;

                if (release.TryGetProperty("tag_name", out var tagElement))
                {
                    string? tag = tagElement.GetString();
                    if (!string.IsNullOrWhiteSpace(tag))
                        return tag;
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogWarning(
                ex,
                "[UPDATE] Could not resolve latest {Channel} release tag",
                GetChannelName(useBetaChannel));
        }

        return null;
    }

    private static bool TryGetBooleanProperty(JsonElement element, string propertyName, out bool value)
    {
        value = false;
        if (!element.TryGetProperty(propertyName, out var property))
            return false;

        if (property.ValueKind != JsonValueKind.True && property.ValueKind != JsonValueKind.False)
            return false;

        value = property.GetBoolean();
        return true;
    }

    private UpdateManager CreateManager(bool useBetaChannel, bool allowVersionDowngrade = false)
    {
        var source = new GithubSource(RepoUrl, null, useBetaChannel);
        var options = new UpdateOptions
        {
            AllowVersionDowngrade = allowVersionDowngrade
        };

        return new UpdateManager(source, options, null);
    }

    private async Task<UpdateManager> CreateManagerForUpdateChecksAsync(
        bool useBetaChannel,
        bool allowVersionDowngrade = false)
    {
        var options = new UpdateOptions
        {
            AllowVersionDowngrade = allowVersionDowngrade
        };

        string? channelTag = await TryResolveLatestChannelTagAsync(useBetaChannel);
        if (!string.IsNullOrWhiteSpace(channelTag))
        {
            string baseUrl = $"{RepoUrl}/releases/download/{channelTag}";
            var source = new SimpleWebSource(baseUrl);
            _logger.LogDebug(
                "[UPDATE] Using {Channel} feed from tag {Tag}",
                GetChannelName(useBetaChannel),
                channelTag);
            return new UpdateManager(source, options, null);
        }

        _logger.LogWarning(
            "[UPDATE] Falling back to GithubSource for {Channel} channel",
            GetChannelName(useBetaChannel));
        return CreateManager(useBetaChannel, allowVersionDowngrade);
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
            var mgr = await CreateManagerForUpdateChecksAsync(useBetaChannel, allowDowngrade);

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
            if (!useBetaChannel && IsPrereleaseVersion(newVersion))
            {
                _logger.LogWarning(
                    "[UPDATE] Ignoring prerelease target {Version} while in stable channel",
                    newVersion);
                return null;
            }

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
            var mgr = await CreateManagerForUpdateChecksAsync(useBetaChannel, allowDowngrade);

            if (!mgr.IsInstalled)
            {
                _logger.LogWarning(
                    "[UPDATE] Cannot apply — not running as installed app");
                return false;
            }

            var info = await mgr.CheckForUpdatesAsync();
            if (info == null) return false;

            string targetVersion = info.TargetFullRelease.Version.ToString();
            if (!useBetaChannel && IsPrereleaseVersion(targetVersion))
            {
                _logger.LogWarning(
                    "[UPDATE] Refusing prerelease download {Version} while in stable channel",
                    targetVersion);
                return false;
            }

            _logger.LogInformation(
                "[UPDATE] Downloading {Channel} package {V}...",
                channel,
                targetVersion);

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
