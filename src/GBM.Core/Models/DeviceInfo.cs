namespace GBM.Core.Models;

public record DeviceInfo
{
    public int VendorId { get; init; }
    public int ProductId { get; init; }
    public int ReleaseNumber { get; init; }
    public string DevicePath { get; init; } = string.Empty;
    public string ModelName { get; init; } = "Unknown";
    public bool IsWireless { get; init; }
    public string CompositeKey => $"{VendorId:X4}_{ProductId:X4}_{ReleaseNumber}_{IsWireless}";
}
