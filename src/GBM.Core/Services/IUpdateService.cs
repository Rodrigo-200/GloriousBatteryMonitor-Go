namespace GBM.Core.Services;

public interface IUpdateService
{
    string CurrentVersion { get; }
    Task<UpdateCheckResult?> CheckForUpdateAsync();
    Task<bool> DownloadAndApplyUpdateAsync(IProgress<int>? progress = null);
}

public record UpdateCheckResult(string NewVersion, string ReleaseUrl);
