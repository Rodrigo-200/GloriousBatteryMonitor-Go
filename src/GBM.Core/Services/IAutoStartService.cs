namespace GBM.Core.Services;

public interface IAutoStartService
{
    bool IsEnabled();
    void SetAutoStart(bool enabled);
}
