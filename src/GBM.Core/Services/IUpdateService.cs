namespace GBM.Core.Services;

public interface IUpdateService
{
    string CurrentVersion { get; }
    Task<UpdateCheckResult?> CheckForUpdateAsync();
    bool IsUpdatePendingRestart();
    Task<bool> DownloadUpdateAsync(IProgress<int>? progress = null);
    bool ApplyPendingUpdateAndRestart(string[]? restartArgs = null);
}

public record UpdateCheckResult(string NewVersion, string ReleaseUrl);
