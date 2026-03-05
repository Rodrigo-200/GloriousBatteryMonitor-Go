using GBM.Core.Models;

namespace GBM.Core.Services;

public interface INotificationService
{
    void ProcessBatteryUpdate(BatteryState current, BatteryState? previous, AppSettings settings);
    event Action<NotificationType, string, string>? NotificationTriggered;
}
