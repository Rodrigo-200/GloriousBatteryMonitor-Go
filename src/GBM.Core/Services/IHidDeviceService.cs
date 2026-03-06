using GBM.Core.Models;

namespace GBM.Core.Services;

public interface IHidDeviceService
{
    List<DeviceInfo> EnumerateDevices();
    (bool Success, int BatteryLevel, bool IsCharging) ReadBattery(DeviceProfile profile);
    DeviceProfile? ProbeDevice(DeviceInfo device);
    bool IsWiredDevicePresent(string modelName);
    bool IsDevicePresent(DeviceProfile profile);
    string GetHidDiagnostics();
    byte[]? CaptureRawReport(DeviceProfile profile);
}
