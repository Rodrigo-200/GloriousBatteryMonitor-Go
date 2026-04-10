using GBM.Core.Models;

namespace GBM.Core.Services;

public interface IBatteryEstimationService
{
    BatteryEstimate GetEstimate(string deviceKey);
    void AddSample(string deviceKey, int level, bool isCharging);
    void Reset(string deviceKey);
    void SetHistoricalRates(string deviceKey, double? dischargeRate, double? chargeRate,
                            int dischargeSessionCount, int chargeSessionCount);
    LearnedRates? GetLearnedRates(string deviceKey);
    void SetChargeCalibration(string deviceKey, double? overshootPercent, int observationCount);
    void ObserveChargeDropAfterUnplug(string deviceKey, int anchorLevel, int measuredLevel, TimeSpan elapsed);
    ChargeCalibration? GetChargeCalibration(string deviceKey);
}
