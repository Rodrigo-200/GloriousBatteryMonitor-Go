using System.Text;
using GBM.Core.Helpers;
using GBM.Core.Models;
using HidSharp;
using Microsoft.Extensions.Logging;

namespace GBM.Core.Services;

public class HidDeviceService : IHidDeviceService
{
    private readonly ILogger<HidDeviceService> _logger;
    private readonly ISettingsService _settingsService;

    // Glorious/Sinowealth battery query command placed after the Report ID byte.
    // Wire format: [ReportID] [0] [0] [DevSel=0x02] [CmdType=0x02] [0] [Func=0x83]
    // DevSel 0x02 = wireless mouse via dongle.
    // CmdType 0x02 = battery query (0x03 = firmware version query).
    // Func 0x83 = battery status (0x81 = firmware version).
    // Confirmed by: AwesomeTy18/GloriousBatteryMonitor, glorious-mouse-battery-system-tray,
    //               glorious-indicator, and OpenRGB SinowealthGMOWController.
    // BuildFeaturePayload copies these bytes starting at payload[1] (after report ID).
    private static readonly byte[] BatteryCommand =
        { 0x00, 0x00, 0x02, 0x02, 0x00, 0x83 };

    // Report IDs to probe — 0x00 is correct for all known Glorious wireless mice
    private static readonly int[] ProbeReportIds = { 0x00, 0x04, 0x03, 0x02, 0x01 };

    // Delay after SetFeature to allow the USB receiver to query the mouse over 2.4 GHz RF.
    // The round-trip: USB poll → RF query → mouse response → RF reply → USB update ≈ 10-50 ms.
    private const int RfRoundTripDelayMs = 100;

    // Longer delay for retry when initial read returns 0%
    private const int RetryDelayMs = 300;

    // ── Pixart (0x093A) battery query candidates ──
    // Model D2 Wireless uses a Pixart chip (VID 0x093A) with different HID commands.
    // Target vendor-specific interfaces (UsagePage 0xFF00) only.

    // Candidate A — PAW3395 Glorious firmware (most commonly reported working)
    // Request:  [RID=0x00][0x11][0xFF][0x03][0xAA][0x00 x 58]  (64-byte feature report)
    // Response: byte[4] = battery %, byte[3] = charge status (0x01 = charging)
    private static readonly byte[] PixartCandidateA =
        { 0x11, 0xFF, 0x03, 0xAA };

    // Candidate B — alternative Pixart HID captures
    // Request:  [RID=0x04][0x01][0x00 x 62]  (64-byte feature report)
    // Response: byte[2] = battery %, byte[1] & 0x80 = charging flag
    private static readonly byte[] PixartCandidateB =
        { 0x01 };
    private const int PixartCandidateBReportId = 0x04;

    public HidDeviceService(ILogger<HidDeviceService> logger, ISettingsService settingsService)
    {
        _logger = logger;
        _settingsService = settingsService;
    }

    public List<DeviceInfo> EnumerateDevices()
    {
        var results = new List<DeviceInfo>();

        try
        {
            var hidDevices = DeviceList.Local.GetHidDevices();

            foreach (var device in hidDevices)
            {
                try
                {
                    int vid = device.VendorID;
                    int pid = device.ProductID;

                    if (!DeviceDatabase.IsKnownVendor(vid))
                        continue;

                    if (DeviceDatabase.TryGetDevice(vid, pid, out string modelName, out bool isWireless))
                    {
                        int maxFeature = 0;
                        int maxInput = 0;
                        int maxOutput = 0;
                        try
                        {
                            maxFeature = device.GetMaxFeatureReportLength();
                            maxInput = device.GetMaxInputReportLength();
                            maxOutput = device.GetMaxOutputReportLength();
                        }
                        catch { }

                        _logger.LogDebug(
                            "[HID] Found {Model} interface: VID=0x{VID:X4} PID=0x{PID:X4} " +
                            "MaxFeature={MaxFeat} MaxInput={MaxIn} MaxOutput={MaxOut} Path={Path}",
                            modelName, vid, pid, maxFeature, maxInput, maxOutput, device.DevicePath);

                        results.Add(new DeviceInfo
                        {
                            VendorId = vid,
                            ProductId = pid,
                            ReleaseNumber = device.ReleaseNumberBcd,
                            DevicePath = device.DevicePath,
                            ModelName = modelName,
                            IsWireless = isWireless
                        });
                    }
                    else
                    {
                        // Log unknown PIDs from known vendors for diagnostic purposes
                        _logger.LogDebug(
                            "[HID] Unknown PID for known vendor: VID=0x{VID:X4} PID=0x{PID:X4} Path={Path}",
                            vid, pid, device.DevicePath);
                    }
                }
                catch (Exception ex)
                {
                    _logger.LogDebug(ex, "Error reading device info for HID device");
                }
            }

            _logger.LogInformation("[HID] Enumerated {Count} Glorious device interface(s)", results.Count);
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[HID] Failed to enumerate HID devices");
        }

        return results;
    }

    public (bool Success, int BatteryLevel, bool IsCharging) ReadBattery(DeviceProfile profile)
    {
        if (profile.Protocol == ChipProtocol.Pixart)
            return ReadBatteryPixart(profile);
        return ReadBatteryWithCommand(profile, BatteryCommand);
    }

    private (bool Success, int BatteryLevel, bool IsCharging) ReadBatteryWithCommand(
        DeviceProfile profile, byte[] command)
    {
        try
        {
            if (_settingsService.Current.SafeHidMode && !IsWhitelistedDevice(profile))
            {
                _logger.LogWarning("[HID] SafeHidMode: device {Key} not whitelisted, skipping",
                    profile.CompositeKey);
                return (false, 0, false);
            }

            var hidDevice = FindDeviceByPath(profile.DevicePath);
            if (hidDevice == null)
            {
                _logger.LogDebug("[HID] Device not found at path: {Path}", profile.DevicePath);
                return (false, 0, false);
            }

            using var stream = hidDevice.Open();
            stream.ReadTimeout = 2000;
            stream.WriteTimeout = 2000;

            byte[] response;

            if (profile.UseFeatureReports)
            {
                int featureLen = profile.ReportLength;
                if (featureLen <= 0)
                {
                    try { featureLen = hidDevice.GetMaxFeatureReportLength(); } catch { featureLen = 64; }
                }
                if (featureLen <= 0) featureLen = 64;

                var payload = BuildFeaturePayload(profile.ReportId, featureLen, command);

                _logger.LogDebug("[HID] SetFeature: ReportId=0x{RID:X2}, Len={Len}, First16={Payload}",
                    profile.ReportId, featureLen,
                    BitConverter.ToString(payload, 0, Math.Min(payload.Length, 16)));

                stream.SetFeature(payload);

                // Wait for the wireless receiver to query the mouse over 2.4 GHz RF
                // and update the feature report with actual battery data.
                Thread.Sleep(RfRoundTripDelayMs);

                // Read feature report back
                response = new byte[featureLen];
                response[0] = (byte)profile.ReportId;
                stream.GetFeature(response);

                LogFullResponse(response, "GetFeature");

                var result = ParseBatteryResponse(response);
                if (result.Success && result.BatteryLevel > 0)
                    return result;

                // If we got 0%, the RF round-trip might need more time. Retry with longer delay.
                if (result.Success && result.BatteryLevel == 0)
                {
                    _logger.LogDebug("[HID] Got Level=0%, retrying with {Delay}ms delay...", RetryDelayMs);
                    Thread.Sleep(RetryDelayMs);

                    response = new byte[featureLen];
                    response[0] = (byte)profile.ReportId;
                    stream.GetFeature(response);

                    LogFullResponse(response, "GetFeature retry");

                    var retry = ParseBatteryResponse(response);
                    if (retry.Success && retry.BatteryLevel > 0)
                        return retry;
                }

                // Return whatever we got (may be 0%)
                return ParseBatteryResponse(response);
            }
            else
            {
                // Use stream (output/input) reports
                int outputLen = profile.ReportLength;
                if (outputLen <= 0)
                {
                    try { outputLen = hidDevice.GetMaxOutputReportLength(); } catch { outputLen = 64; }
                }
                if (outputLen <= 0) outputLen = 64;

                var payload = BuildFeaturePayload(profile.ReportId, outputLen, command);

                _logger.LogDebug("[HID] Write: ReportId=0x{RID:X2}, Len={Len}", profile.ReportId, outputLen);

                stream.Write(payload);

                // Also add delay for stream reads — wireless receiver needs RF round-trip time
                Thread.Sleep(RfRoundTripDelayMs);

                response = stream.Read();

                LogFullResponse(response, "Read");

                var result = ParseBatteryResponse(response);
                if (result.Success && result.BatteryLevel > 0)
                    return result;

                // Retry with longer delay
                if (result.Success && result.BatteryLevel == 0)
                {
                    _logger.LogDebug("[HID] Got Level=0% on stream, retrying...");
                    stream.Write(payload);
                    Thread.Sleep(RetryDelayMs);
                    response = stream.Read();

                    LogFullResponse(response, "Read retry");
                    return ParseBatteryResponse(response);
                }

                return result;
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[HID] ReadBattery failed for {Path}", profile.DevicePath);
            return (false, 0, false);
        }
    }

    private (bool Success, int BatteryLevel, bool IsCharging) ParseBatteryResponse(byte[] response)
    {
        // Sinowealth battery response format (fixed offsets, confirmed by multiple projects):
        //   [0] ReportID  [1] Status  [2..5] echo  [6] 0x83  [7] Charging  [8] Level%
        //
        // Status byte meanings:
        //   0xA1 = normal (mouse awake), 0xA2 = waking up, 0xA4 = asleep
        //
        // We require byte[6] == 0x83 to confirm this is a valid battery response.

        if (response.Length < 9)
        {
            _logger.LogDebug("[HID] Response too short: {Len} bytes", response.Length);
            return (false, 0, false);
        }

        byte status = response[1];

        // Check for 0x83 function marker at the expected fixed position
        if (response[6] == 0x83)
        {
            // Charging byte: 0x00 = not charging, any non-zero = charging/charged.
            // Known values: 0x01 = actively charging, 0x02 = fully charged on cable,
            // 0x03 = charge complete. Treat all non-zero as "on charger".
            byte chargeByte = response[7];
            bool isCharging = chargeByte != 0x00;
            int level = response[8];

            if (level >= 0 && level <= 100)
            {
                _logger.LogDebug(
                    "[HID] Battery: Status=0x{Status:X2}, ChargeByte=0x{ChargeByte:X2}, Level={Level}%, Charging={Charging}",
                    status, chargeByte, level, isCharging);
                return (true, level, isCharging);
            }
        }

        _logger.LogDebug("[HID] No valid battery data (Status=0x{Status:X2}, Byte6=0x{B6:X2}) in {Len}-byte response",
            status, response[6], response.Length);
        return (false, 0, false);
    }

    // ── Pixart protocol methods ──

    private (bool Success, int BatteryLevel, bool IsCharging) ReadBatteryPixart(DeviceProfile profile)
    {
        try
        {
            if (_settingsService.Current.SafeHidMode && !IsWhitelistedDevice(profile))
                return (false, 0, false);

            var hidDevice = FindDeviceByPath(profile.DevicePath);
            if (hidDevice == null)
                return (false, 0, false);

            // CandidateD and CandidateE open their own streams on the HidDevice directly
            if (profile.PixartMethod == PixartBatteryMethod.CandidateD)
                return TryPixartCandidateD(hidDevice, _logger);
            if (profile.PixartMethod == PixartBatteryMethod.CandidateE)
                return TryPixartCandidateE(hidDevice, _logger);

            using var stream = hidDevice.Open();
            stream.ReadTimeout = 2000;
            stream.WriteTimeout = 2000;

            int featureLen = profile.ReportLength;
            if (featureLen <= 0)
            {
                try { featureLen = hidDevice.GetMaxFeatureReportLength(); } catch { featureLen = 64; }
            }
            if (featureLen <= 0) featureLen = 64;

            switch (profile.PixartMethod)
            {
                case PixartBatteryMethod.CandidateA:
                    return TryPixartCandidateA(stream, featureLen);
                case PixartBatteryMethod.CandidateB:
                    return TryPixartCandidateB(stream, featureLen);
                case PixartBatteryMethod.CandidateC:
                    return TryPixartCandidateC(stream);
                default:
                    // Unknown method — try all candidates in order
                    var d = TryPixartCandidateD(hidDevice, _logger);
                    if (d.Success) return d;
                    var a = TryPixartCandidateA(stream, featureLen);
                    if (a.Success) return a;
                    var b = TryPixartCandidateB(stream, featureLen);
                    if (b.Success) return b;
                    return TryPixartCandidateC(stream);
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[Pixart] ReadBattery failed for {Path}", profile.DevicePath);
            return (false, 0, false);
        }
    }

    private (bool Success, int BatteryLevel, bool IsCharging) TryPixartCandidateA(
        HidStream stream, int featureLen)
    {
        try
        {
            var payload = new byte[featureLen];
            payload[0] = 0x00; // Report ID
            for (int i = 0; i < PixartCandidateA.Length && (1 + i) < payload.Length; i++)
                payload[1 + i] = PixartCandidateA[i];

            stream.SetFeature(payload);
            Thread.Sleep(RfRoundTripDelayMs);

            var response = new byte[featureLen];
            response[0] = 0x00;
            stream.GetFeature(response);

            LogFullResponse(response, "Pixart-A GetFeature");

            if (response.Length >= 5)
            {
                int level = response[4];
                bool isCharging = response[3] == 0x01;

                if (level >= 1 && level <= 100)
                {
                    _logger.LogDebug("[Pixart] Candidate A: battery={Level}%, charging={Charging}",
                        level, isCharging);
                    return (true, level, isCharging);
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[Pixart] Candidate A failed");
        }
        return (false, 0, false);
    }

    private (bool Success, int BatteryLevel, bool IsCharging) TryPixartCandidateB(
        HidStream stream, int featureLen)
    {
        try
        {
            var payload = new byte[featureLen];
            payload[0] = (byte)PixartCandidateBReportId;
            for (int i = 0; i < PixartCandidateB.Length && (1 + i) < payload.Length; i++)
                payload[1 + i] = PixartCandidateB[i];

            stream.SetFeature(payload);
            Thread.Sleep(RfRoundTripDelayMs);

            var response = new byte[featureLen];
            response[0] = (byte)PixartCandidateBReportId;
            stream.GetFeature(response);

            LogFullResponse(response, "Pixart-B GetFeature");

            if (response.Length >= 3)
            {
                int level = response[2];
                bool isCharging = (response[1] & 0x80) != 0;

                if (level >= 1 && level <= 100)
                {
                    _logger.LogDebug("[Pixart] Candidate B: battery={Level}%, charging={Charging}",
                        level, isCharging);
                    return (true, level, isCharging);
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[Pixart] Candidate B failed");
        }
        return (false, 0, false);
    }

    private (bool Success, int BatteryLevel, bool IsCharging) TryPixartCandidateC(HidStream stream)
    {
        // Passive input report read — some firmware broadcasts battery reports.
        try
        {
            var response = stream.Read();
            LogFullResponse(response, "Pixart-C passive read");

            // Check positions 1-5 for a plausible battery percentage
            for (int i = 1; i < Math.Min(6, response.Length); i++)
            {
                int val = response[i];
                if (val >= 1 && val <= 100)
                {
                    _logger.LogDebug("[Pixart] Candidate C: possible battery={Val}% at offset {Offset}",
                        val, i);
                    return (true, val, false);
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[Pixart] Candidate C (passive read) failed");
        }
        return (false, 0, false);
    }

    private (bool Success, int BatteryLevel, bool IsCharging) TryPixartCandidateD(
        HidDevice device, ILogger logger)
    {
        // GetFeature-only probe: read feature reports WITHOUT a prior SetFeature write.
        // The mi_01&col01 interface on 0x093A:0x824D rejects SetFeature entirely,
        // but may respond to GetFeature with battery data already populated by firmware.
        for (byte reportId = 0x00; reportId <= 0x0F; reportId++)
        {
            try
            {
                using var stream = device.Open();
                stream.ReadTimeout = 2000;

                var buf = new byte[65]; // 64 bytes + report ID byte
                buf[0] = reportId;

                stream.GetFeature(buf);

                logger.LogDebug(
                    "[Pixart CandidateD] RID=0x{RID:X2} raw: {Bytes}",
                    reportId,
                    BitConverter.ToString(buf, 0, Math.Min(16, buf.Length)));

                // Check every byte position 1-10 for a plausible battery level
                for (int i = 1; i <= 10 && i < buf.Length; i++)
                {
                    if (buf[i] >= 1 && buf[i] <= 100)
                    {
                        // Tentative hit — also check adjacent byte for charge flag
                        bool charging = (i + 1 < buf.Length && buf[i + 1] == 0x01)
                                     || (i - 1 >= 0 && buf[i - 1] == 0x01);
                        logger.LogInformation(
                            "[Pixart CandidateD] plausible battery={Level} at byte[{Idx}], " +
                            "charging={Charging}, RID=0x{RID:X2}",
                            buf[i], i, charging, reportId);
                        return (true, buf[i], charging);
                    }
                }
            }
            catch (Exception ex)
            {
                logger.LogDebug(
                    "[Pixart CandidateD] RID=0x{RID:X2} failed: {Msg}",
                    reportId, ex.Message);
            }
        }
        return (false, 0, false);
    }

    private (bool Success, int BatteryLevel, bool IsCharging) TryPixartCandidateE(
        HidDevice device, ILogger logger)
    {
        // Passive input report read on interfaces with MaxInput > 0.
        // Candidate C was incorrectly attempted on col01 (MaxInput=0).
        // This targets col05 (MaxInput=8) which can actually stream input reports.
        try
        {
            using var stream = device.Open();
            stream.ReadTimeout = 2000;

            var buf = stream.Read(); // blocking read, 2s timeout

            logger.LogDebug(
                "[Pixart CandidateE] passive read raw: {Bytes}",
                BitConverter.ToString(buf, 0, Math.Min(16, buf.Length)));

            for (int i = 1; i < Math.Min(buf.Length, 10); i++)
            {
                if (buf[i] >= 1 && buf[i] <= 100)
                {
                    bool charging = i + 1 < buf.Length && buf[i + 1] == 0x01;
                    logger.LogInformation(
                        "[Pixart CandidateE] plausible battery={Level} at byte[{Idx}], " +
                        "charging={Charging}",
                        buf[i], i, charging);
                    return (true, buf[i], charging);
                }
            }
        }
        catch (Exception ex)
        {
            logger.LogDebug("[Pixart CandidateE] failed: {Msg}", ex.Message);
        }
        return (false, 0, false);
    }

    private DeviceProfile? ProbeDevicePixart(HidDevice hidDevice, DeviceInfo device)
    {
        int maxFeatureLen = 0;
        int maxInputLen = 0;
        try { maxFeatureLen = hidDevice.GetMaxFeatureReportLength(); } catch { }
        try { maxInputLen = hidDevice.GetMaxInputReportLength(); } catch { }

        // Check if this interface has a vendor-specific usage page (0xFF00).
        // Skip standard HID interfaces (mouse 0x0001, consumer 0x000C).
        int usagePage = GetPrimaryUsagePage(hidDevice);
        if (usagePage != 0 && usagePage != 0xFF00)
        {
            _logger.LogDebug(
                "[Pixart] Skipping interface with UsagePage=0x{UP:X4} for {Model} at {Path}",
                usagePage, device.ModelName, device.DevicePath);
            return null;
        }

        _logger.LogDebug(
            "[Pixart] Probing {Model} at {Path}: UsagePage=0x{UP:X4}, MaxFeature={MF}, MaxInput={MI}",
            device.ModelName, device.DevicePath, usagePage, maxFeatureLen, maxInputLen);

        if (maxFeatureLen <= 0 && maxInputLen <= 0)
        {
            _logger.LogDebug("[Pixart] No feature or input reports on this interface, skipping");
            return null;
        }

        DeviceProfile MakeProfile(PixartBatteryMethod method, string path, int reportLen, bool useFeature) => new()
        {
            CompositeKey = device.CompositeKey,
            DevicePath = path,
            ReportId = 0x00,
            ReportLength = reportLen,
            UseFeatureReports = useFeature,
            VendorId = device.VendorId,
            ProductId = device.ProductId,
            ModelName = device.ModelName,
            LastSeen = DateTime.UtcNow,
            Protocol = ChipProtocol.Pixart,
            PixartMethod = method
        };

        // ── Feature report probes (requires MaxFeature > 0) ──
        if (maxFeatureLen > 0)
        {
            // Candidate D first: GetFeature-only (no SetFeature write).
            // SetFeature is known-rejected on 0x093A:0x824D col01.
            _logger.LogInformation(
                "[Pixart probe] 0x{VID:X4}:0x{PID:X4} — trying candidate D (GetFeature-only) on {Path}...",
                device.VendorId, device.ProductId, device.DevicePath);

            var resultD = TryPixartCandidateD(hidDevice, _logger);
            if (resultD.Success && resultD.BatteryLevel >= 1)
            {
                _logger.LogInformation(
                    "[Pixart probe] candidate D returned battery={Level}, charging={Charging} — profile saved",
                    resultD.BatteryLevel, resultD.IsCharging);
                return MakeProfile(PixartBatteryMethod.CandidateD, device.DevicePath, maxFeatureLen, true);
            }

            // Candidate A: SetFeature + GetFeature (PAW3395 command 0x11)
            _logger.LogDebug("[Pixart probe] candidate D no result, trying candidate A...");
            try
            {
                using var stream = hidDevice.Open();
                stream.ReadTimeout = 2000;
                stream.WriteTimeout = 2000;

                var resultA = TryPixartCandidateA(stream, maxFeatureLen);
                if (resultA.Success && resultA.BatteryLevel >= 1)
                {
                    _logger.LogInformation(
                        "[Pixart probe] candidate A returned battery={Level}, charging={Charging} — profile saved",
                        resultA.BatteryLevel, resultA.IsCharging);
                    return MakeProfile(PixartBatteryMethod.CandidateA, device.DevicePath, maxFeatureLen, true);
                }

                // Candidate B: SetFeature + GetFeature (command 0x04/0x01)
                _logger.LogDebug("[Pixart probe] candidate A no result, trying candidate B...");
                var resultB = TryPixartCandidateB(stream, maxFeatureLen);
                if (resultB.Success && resultB.BatteryLevel >= 1)
                {
                    _logger.LogInformation(
                        "[Pixart probe] candidate B returned battery={Level}, charging={Charging} — profile saved",
                        resultB.BatteryLevel, resultB.IsCharging);
                    return MakeProfile(PixartBatteryMethod.CandidateB, device.DevicePath, maxFeatureLen, true);
                }
            }
            catch (Exception ex)
            {
                _logger.LogDebug(ex, "[Pixart probe] Error during A/B probing on {Model}", device.ModelName);
            }
        }

        // ── Input report probes (requires MaxInput > 0) ──
        if (maxInputLen > 0)
        {
            _logger.LogDebug("[Pixart probe] trying candidate E (passive stream read) on {Path}...",
                device.DevicePath);

            var resultE = TryPixartCandidateE(hidDevice, _logger);
            if (resultE.Success && resultE.BatteryLevel >= 1)
            {
                _logger.LogInformation(
                    "[Pixart probe] candidate E returned battery={Level}, charging={Charging} — profile saved",
                    resultE.BatteryLevel, resultE.IsCharging);
                return MakeProfile(PixartBatteryMethod.CandidateE, device.DevicePath, maxInputLen, false);
            }
        }

        _logger.LogDebug("[Pixart probe] No working candidate for {Model} at {Path}",
            device.ModelName, device.DevicePath);
        return null;
    }

    public DeviceProfile? ProbeDevice(DeviceInfo device)
    {
        try
        {
            var hidDevice = FindDeviceByPath(device.DevicePath);
            if (hidDevice == null)
            {
                _logger.LogDebug("[HID] Cannot probe: device not found at path {Path}", device.DevicePath);
                return null;
            }

            // Pixart devices (0x093A) use a different protocol entirely
            if (DeviceDatabase.IsPixartDevice(device.VendorId))
                return ProbeDevicePixart(hidDevice, device);

            int maxFeatureLen = 0;
            int maxOutputLen = 0;
            try { maxFeatureLen = hidDevice.GetMaxFeatureReportLength(); } catch { }
            try { maxOutputLen = hidDevice.GetMaxOutputReportLength(); } catch { }

            _logger.LogDebug("[HID] Probing {Model} at {Path}: MaxFeature={MF}, MaxOutput={MO}",
                device.ModelName, device.DevicePath, maxFeatureLen, maxOutputLen);

            // Try each report ID × transport combination
            foreach (int reportId in ProbeReportIds)
            {
                // Try feature reports if the interface supports them
                if (maxFeatureLen > 0)
                {
                    var profile = new DeviceProfile
                    {
                        CompositeKey = device.CompositeKey,
                        DevicePath = device.DevicePath,
                        ReportId = reportId,
                        ReportLength = maxFeatureLen,
                        UseFeatureReports = true,
                        VendorId = device.VendorId,
                        ProductId = device.ProductId,
                        ModelName = device.ModelName,
                        LastSeen = DateTime.UtcNow
                    };

                    var result = ReadBatteryWithCommand(profile, BatteryCommand);

                    // Success=true means byte[6]==0x83 in the response, confirming
                    // this is the correct Sinowealth interface.
                    // Level may be 0% if the mouse is asleep (status 0xA4) — that's OK,
                    // the next poll when the mouse wakes will get real data.
                    if (result.Success)
                    {
                        _logger.LogInformation(
                            "[HID] Probe SUCCESS: {Model} ReportId=0x{RID:X2} (feature), Level={Level}%",
                            device.ModelName, reportId, result.BatteryLevel);
                        return profile;
                    }
                }

                // Try output/input reports as fallback
                if (maxOutputLen > 0)
                {
                    var profile = new DeviceProfile
                    {
                        CompositeKey = device.CompositeKey,
                        DevicePath = device.DevicePath,
                        ReportId = reportId,
                        ReportLength = maxOutputLen,
                        UseFeatureReports = false,
                        VendorId = device.VendorId,
                        ProductId = device.ProductId,
                        ModelName = device.ModelName,
                        LastSeen = DateTime.UtcNow
                    };

                    var result = ReadBatteryWithCommand(profile, BatteryCommand);
                    if (result.Success && result.BatteryLevel > 0)
                    {
                        _logger.LogInformation(
                            "[HID] Probe SUCCESS: {Model} ReportId=0x{RID:X2} (stream), Level={Level}%",
                            device.ModelName, reportId, result.BatteryLevel);
                        return profile;
                    }
                }
            }

            // If standard probing failed, try a passive read (GetFeature without SetFeature).
            // Some receivers continuously update battery status in their feature reports.
            if (maxFeatureLen > 0)
            {
                _logger.LogDebug("[HID] Trying passive feature read for {Model}...", device.ModelName);
                var passiveResult = TryPassiveFeatureRead(hidDevice, maxFeatureLen, device);
                if (passiveResult != null)
                    return passiveResult;
            }

            _logger.LogDebug("[HID] Probe FAILED for {Model} at {Path}", device.ModelName, device.DevicePath);
            return null;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[HID] Error probing device {Model}", device.ModelName);
            return null;
        }
    }

    /// <summary>
    /// Try reading feature reports without sending a command first.
    /// Some wireless receivers continuously update battery data in their feature reports.
    /// </summary>
    private DeviceProfile? TryPassiveFeatureRead(HidDevice hidDevice, int featureLen, DeviceInfo device)
    {
        try
        {
            using var stream = hidDevice.Open();
            stream.ReadTimeout = 2000;

            foreach (int reportId in ProbeReportIds)
            {
                try
                {
                    var response = new byte[featureLen];
                    response[0] = (byte)reportId;
                    stream.GetFeature(response);

                    LogFullResponse(response, $"Passive GetFeature RID=0x{reportId:X2}");

                    var result = ParseBatteryResponse(response);
                    if (result.Success && result.BatteryLevel > 0)
                    {
                        _logger.LogInformation(
                            "[HID] Passive read SUCCESS: {Model} ReportId=0x{RID:X2}, Level={Level}%",
                            device.ModelName, reportId, result.BatteryLevel);

                        return new DeviceProfile
                        {
                            CompositeKey = device.CompositeKey,
                            DevicePath = device.DevicePath,
                            ReportId = reportId,
                            ReportLength = featureLen,
                            UseFeatureReports = true,
                            VendorId = device.VendorId,
                            ProductId = device.ProductId,
                            ModelName = device.ModelName,
                            LastSeen = DateTime.UtcNow
                        };
                    }
                }
                catch
                {
                    // This report ID doesn't support passive reads; skip
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[HID] Passive feature read failed for {Model}", device.ModelName);
        }

        return null;
    }

    public bool IsWiredDevicePresent(string modelName)
    {
        try
        {
            var wiredPids = DeviceDatabase.GetWiredPidsForModel(modelName);
            if (wiredPids.Count == 0)
                return false;

            var hidDevices = DeviceList.Local.GetHidDevices();
            foreach (var device in hidDevices)
            {
                int vid = device.VendorID;
                int pid = device.ProductID;
                foreach (var (wiredVid, wiredPid) in wiredPids)
                {
                    if (vid == wiredVid && pid == wiredPid)
                    {
                        _logger.LogDebug("[HID] Wired device detected for {Model}: VID=0x{VID:X4} PID=0x{PID:X4}",
                            modelName, vid, pid);
                        return true;
                    }
                }
            }
        }
        catch (Exception ex)
        {
            _logger.LogDebug(ex, "[HID] Error checking for wired device presence");
        }

        return false;
    }

    public string GetHidDiagnostics()
    {
        var sb = new StringBuilder();
        sb.AppendLine("=== HID Device Diagnostics ===");
        sb.AppendLine($"Timestamp: {DateTime.UtcNow:O}");
        sb.AppendLine();

        try
        {
            var allDevices = DeviceList.Local.GetHidDevices();
            int gloriousCount = 0;
            sb.AppendLine($"Total HID devices on system: {allDevices.Count()}");
            sb.AppendLine();

            int index = 0;
            foreach (var device in allDevices)
            {
                try
                {
                    bool isKnown = DeviceDatabase.IsKnownVendor(device.VendorID);
                    if (!isKnown) continue;

                    gloriousCount++;
                    sb.AppendLine($"--- Glorious Interface #{index++} ---");
                    sb.AppendLine($"  VID: 0x{device.VendorID:X4}");
                    sb.AppendLine($"  PID: 0x{device.ProductID:X4}");
                    sb.AppendLine($"  Product: {SafeGetProductName(device)}");
                    sb.AppendLine($"  Release: 0x{device.ReleaseNumberBcd:X4}");
                    sb.AppendLine($"  Path: {device.DevicePath}");

                    if (DeviceDatabase.TryGetDevice(device.VendorID, device.ProductID,
                            out string model, out bool wireless))
                    {
                        sb.AppendLine($"  Model: {model} (wireless={wireless})");
                    }
                    else
                    {
                        sb.AppendLine($"  Model: Unknown PID");
                    }

                    try
                    {
                        sb.AppendLine($"  Max Feature Report: {device.GetMaxFeatureReportLength()}");
                        sb.AppendLine($"  Max Input Report: {device.GetMaxInputReportLength()}");
                        sb.AppendLine($"  Max Output Report: {device.GetMaxOutputReportLength()}");
                    }
                    catch
                    {
                        sb.AppendLine("  Report lengths: unavailable");
                    }

                    try
                    {
                        var reportDesc = device.GetRawReportDescriptor();
                        sb.AppendLine($"  Report Descriptor: {reportDesc.Length} bytes");
                        sb.AppendLine($"  Report Descriptor Hex: {BitConverter.ToString(reportDesc)}");
                    }
                    catch { }

                    // Log usage page and protocol type
                    int usagePage = GetPrimaryUsagePage(device);
                    bool isPixart = DeviceDatabase.IsPixartDevice(device.VendorID);
                    sb.AppendLine($"  UsagePage: 0x{usagePage:X4}");
                    sb.AppendLine($"  Protocol: {(isPixart ? "Pixart" : "Sinowealth")}");

                    // Try a quick battery probe on each interface
                    try
                    {
                        using var stream = device.Open();
                        stream.ReadTimeout = 1000;
                        stream.WriteTimeout = 1000;

                        int featureLen = device.GetMaxFeatureReportLength();
                        if (featureLen > 0)
                        {
                            if (isPixart)
                            {
                                // Pixart probe — try each candidate
                                try
                                {
                                    var resultD = TryPixartCandidateD(device, _logger);
                                    sb.AppendLine($"  Pixart Candidate D: {(resultD.Success ? $"battery={resultD.BatteryLevel}%, charging={resultD.IsCharging}" : "no valid response")}");
                                }
                                catch (Exception pex)
                                {
                                    sb.AppendLine($"  Pixart Candidate D: FAILED ({pex.GetType().Name}: {pex.Message})");
                                }
                                try
                                {
                                    var resultA = TryPixartCandidateA(stream, featureLen);
                                    sb.AppendLine($"  Pixart Candidate A: {(resultA.Success ? $"battery={resultA.BatteryLevel}%, charging={resultA.IsCharging}" : "no valid response")}");
                                }
                                catch (Exception pex)
                                {
                                    sb.AppendLine($"  Pixart Candidate A: FAILED ({pex.GetType().Name}: {pex.Message})");
                                }
                                try
                                {
                                    var resultB = TryPixartCandidateB(stream, featureLen);
                                    sb.AppendLine($"  Pixart Candidate B: {(resultB.Success ? $"battery={resultB.BatteryLevel}%, charging={resultB.IsCharging}" : "no valid response")}");
                                }
                                catch (Exception pex)
                                {
                                    sb.AppendLine($"  Pixart Candidate B: FAILED ({pex.GetType().Name}: {pex.Message})");
                                }
                            }
                            else
                            {
                                // Sinowealth probe
                                foreach (int rid in new[] { 0x04, 0x00 })
                                {
                                    try
                                    {
                                        var payload = BuildFeaturePayload(rid, featureLen, BatteryCommand);
                                        stream.SetFeature(payload);
                                        Thread.Sleep(RfRoundTripDelayMs);
                                        var resp = new byte[featureLen];
                                        resp[0] = (byte)rid;
                                        stream.GetFeature(resp);
                                        sb.AppendLine($"  Probe RID=0x{rid:X2}: {FormatResponseBytes(resp)}");
                                    }
                                    catch (Exception pex)
                                    {
                                        sb.AppendLine($"  Probe RID=0x{rid:X2}: FAILED ({pex.GetType().Name}: {pex.Message})");
                                    }
                                }
                            }
                        }
                    }
                    catch (Exception ex)
                    {
                        sb.AppendLine($"  Open/probe: FAILED ({ex.GetType().Name}: {ex.Message})");
                    }

                    sb.AppendLine();
                }
                catch (Exception ex)
                {
                    sb.AppendLine($"  Error reading device: {ex.Message}");
                    sb.AppendLine();
                }
            }

            sb.AppendLine($"Total Glorious interfaces: {gloriousCount}");
        }
        catch (Exception ex)
        {
            sb.AppendLine($"Error enumerating devices: {ex.Message}");
        }

        return sb.ToString();
    }

    public byte[]? CaptureRawReport(DeviceProfile profile)
    {
        try
        {
            var hidDevice = FindDeviceByPath(profile.DevicePath);
            if (hidDevice == null)
            {
                _logger.LogDebug("[HID] CaptureRawReport: device not found");
                return null;
            }

            using var stream = hidDevice.Open();
            stream.ReadTimeout = 2000;
            stream.WriteTimeout = 2000;

            int featureLen = profile.ReportLength;
            if (featureLen <= 0)
            {
                try { featureLen = hidDevice.GetMaxFeatureReportLength(); } catch { featureLen = 64; }
            }
            if (featureLen <= 0) featureLen = 64;

            var payload = BuildFeaturePayload(profile.ReportId, featureLen, BatteryCommand);

            byte[] response;

            if (profile.UseFeatureReports)
            {
                stream.SetFeature(payload);
                Thread.Sleep(RfRoundTripDelayMs);
                response = new byte[featureLen];
                response[0] = (byte)profile.ReportId;
                stream.GetFeature(response);
            }
            else
            {
                stream.Write(payload);
                Thread.Sleep(RfRoundTripDelayMs);
                response = stream.Read();
            }

            _logger.LogDebug("[HID] Captured raw report: {Response}", FormatResponseBytes(response));
            return response;
        }
        catch (Exception ex)
        {
            _logger.LogError(ex, "[HID] Failed to capture raw report");
            return null;
        }
    }

    private static byte[] BuildFeaturePayload(int reportId, int length, byte[] command)
    {
        var payload = new byte[length];
        payload[0] = (byte)reportId;

        // Write command bytes starting at position 1 (after report ID byte)
        for (int i = 0; i < command.Length && (1 + i) < payload.Length; i++)
        {
            payload[1 + i] = command[i];
        }

        return payload;
    }

    private void LogFullResponse(byte[] response, string label)
    {
        // Log first 16 bytes always
        _logger.LogDebug("[HID] {Label} ({Len} bytes): {First16}",
            label, response.Length,
            BitConverter.ToString(response, 0, Math.Min(response.Length, 16)));

        // Also log any non-zero bytes beyond position 16 for diagnostics
        var nonZeroPositions = new List<string>();
        for (int i = 16; i < response.Length; i++)
        {
            if (response[i] != 0)
                nonZeroPositions.Add($"[{i}]=0x{response[i]:X2}");
        }

        if (nonZeroPositions.Count > 0)
        {
            _logger.LogDebug("[HID] {Label} non-zero bytes beyond offset 16: {Bytes}",
                label, string.Join(", ", nonZeroPositions));
        }
    }

    private static string FormatResponseBytes(byte[] response)
    {
        // Show first 16 bytes and any non-zero bytes beyond that
        var sb = new StringBuilder();
        sb.Append(BitConverter.ToString(response, 0, Math.Min(response.Length, 16)));

        bool hasMore = false;
        for (int i = 16; i < response.Length; i++)
        {
            if (response[i] != 0)
            {
                if (!hasMore)
                {
                    sb.Append(" ...");
                    hasMore = true;
                }
                sb.Append($" [{i}]=0x{response[i]:X2}");
            }
        }

        return sb.ToString();
    }

    private static HidDevice? FindDeviceByPath(string devicePath)
    {
        try
        {
            return DeviceList.Local.GetHidDevices()
                .FirstOrDefault(d => string.Equals(d.DevicePath, devicePath, StringComparison.OrdinalIgnoreCase));
        }
        catch
        {
            return null;
        }
    }

    /// <summary>
    /// Extract the primary Usage Page from a HID device's raw report descriptor.
    /// Returns 0 if the descriptor cannot be parsed.
    /// </summary>
    private static int GetPrimaryUsagePage(HidDevice device)
    {
        try
        {
            var descriptor = device.GetRawReportDescriptor();
            for (int i = 0; i < descriptor.Length; i++)
            {
                byte tag = descriptor[i];
                // Short item: Usage Page (1 byte) — tag 0x05
                if (tag == 0x05 && i + 1 < descriptor.Length)
                    return descriptor[i + 1];
                // Short item: Usage Page (2 bytes) — tag 0x06
                if (tag == 0x06 && i + 2 < descriptor.Length)
                    return descriptor[i + 1] | (descriptor[i + 2] << 8);
            }
        }
        catch { }
        return 0;
    }

    private static bool IsWhitelistedDevice(DeviceProfile profile)
    {
        return DeviceDatabase.TryGetDevice(profile.VendorId, profile.ProductId, out _, out _);
    }

    private static string SafeGetProductName(HidDevice device)
    {
        try
        {
            return device.GetProductName() ?? "N/A";
        }
        catch
        {
            return "N/A";
        }
    }
}
