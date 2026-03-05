using GBM.Core.Models;

namespace GBM.Core.Services;

public interface ISettingsService
{
    AppSettings Current { get; }
    void Load();
    void Save(AppSettings settings);
    event Action<AppSettings>? SettingsChanged;
    string GetAppDataPath();
}
