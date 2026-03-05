using GBM.Core.Models;

namespace GBM.Core.Services;

public interface IBatteryMonitorService
{
    BatteryState CurrentState { get; }
    BatteryEstimate CurrentEstimate { get; }
    bool IsRunning { get; }
    Task StartAsync(CancellationToken cancellationToken = default);
    Task StopAsync();
    void TriggerRescan();
    event Action<BatteryState>? BatteryStateChanged;
    event Action<BatteryEstimate>? EstimateChanged;
}
