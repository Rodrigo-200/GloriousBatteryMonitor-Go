using GBM.Core.Models;

namespace GBM.Core.Services;

public interface IStorageService
{
    ChargeData LoadChargeData();
    void SaveChargeData(ChargeData data);
    List<DeviceProfile> LoadProfiles();
    void SaveProfiles(List<DeviceProfile> profiles);
    void ClearProfiles();
    void AddBatterySample(string deviceKey, int level, bool isCharging);
    DeviceChargeData? GetDeviceChargeData(string deviceKey);
    void UpdateChargeInfo(string deviceKey, int level, DateTime? chargeTime);
    void UpdateLearnedRates(string deviceKey, double? dischargeRate, double? chargeRate,
                            int dischargeSessions, int chargeSessions, bool forceSave = false);
}
