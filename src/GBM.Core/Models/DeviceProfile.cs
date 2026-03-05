namespace GBM.Core.Models;

public enum ChipProtocol { Sinowealth, Pixart }

public enum PixartBatteryMethod { CandidateA, CandidateB, CandidateC, CandidateD, CandidateE, CandidateF }

public class DeviceProfile
{
    public string CompositeKey { get; set; } = string.Empty;
    public string DevicePath { get; set; } = string.Empty;
    public int ReportId { get; set; }
    public int ReportLength { get; set; }
    public bool UseFeatureReports { get; set; } = true;
    public int VendorId { get; set; }
    public int ProductId { get; set; }
    public string ModelName { get; set; } = string.Empty;
    public DateTime LastSeen { get; set; }
    public ChipProtocol Protocol { get; set; } = ChipProtocol.Sinowealth;
    public PixartBatteryMethod? PixartMethod { get; set; }

    /// <summary>
    /// For CandidateF: the sibling interface path used for input report reads
    /// while the primary DevicePath is used for feature report triggers.
    /// </summary>
    public string? SiblingDevicePath { get; set; }
}
