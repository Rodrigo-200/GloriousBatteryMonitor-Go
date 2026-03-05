namespace GBM.Core.Helpers;

public static class DeviceDatabase
{
    public const int GloriousVendorId = 0x258A;
    public const int AlternateVendorId = 0x093A;

    private static readonly Dictionary<(int VendorId, int ProductId), (string ModelName, bool IsWireless)> Devices = new()
    {
        // Model O
        { (GloriousVendorId, 0x2011), ("Model O", false) },
        { (GloriousVendorId, 0x2013), ("Model O", true) },
        // Model O-
        { (GloriousVendorId, 0x2019), ("Model O-", false) },
        { (GloriousVendorId, 0x2024), ("Model O-", true) },
        // Model O Pro
        { (GloriousVendorId, 0x2017), ("Model O Pro", false) },
        { (GloriousVendorId, 0x2018), ("Model O Pro", true) },
        // Model O2
        { (GloriousVendorId, 0x2009), ("Model O2", false) },
        { (GloriousVendorId, 0x200B), ("Model O2", true) },
        // Model D
        { (GloriousVendorId, 0x2012), ("Model D", false) },
        { (GloriousVendorId, 0x2023), ("Model D", true) },
        // Model D-
        { (GloriousVendorId, 0x2015), ("Model D-", false) },
        { (GloriousVendorId, 0x2025), ("Model D-", true) },
        // Model D2
        { (GloriousVendorId, 0x2031), ("Model D2", false) },
        { (GloriousVendorId, 0x2033), ("Model D2", true) },
        // Model I
        { (GloriousVendorId, 0x2036), ("Model I", false) },
        { (GloriousVendorId, 0x2037), ("Model I", true) },
        { (GloriousVendorId, 0x2046), ("Model I", true) }, // Receiver
        // Model I2
        { (GloriousVendorId, 0x2014), ("Model I2", false) },
        { (GloriousVendorId, 0x2016), ("Model I2", true) },
        // Alternate vendor - Model D2 Wireless
        { (AlternateVendorId, 0x824D), ("Model D2", true) },
    };

    public static bool IsKnownVendor(int vendorId) =>
        vendorId == GloriousVendorId || vendorId == AlternateVendorId;

    public static bool IsPixartDevice(int vendorId) => vendorId == AlternateVendorId;

    public static bool TryGetDevice(int vendorId, int productId, out string modelName, out bool isWireless)
    {
        if (Devices.TryGetValue((vendorId, productId), out var info))
        {
            modelName = info.ModelName;
            isWireless = info.IsWireless;
            return true;
        }
        modelName = "Unknown";
        isWireless = false;
        return false;
    }

    public static IReadOnlyDictionary<(int VendorId, int ProductId), (string ModelName, bool IsWireless)> GetAllDevices() => Devices;

    /// <summary>
    /// Returns wired (VendorId, ProductId) pairs for the given model name.
    /// Used to detect whether the wired variant of a wireless device is connected (charging signal).
    /// </summary>
    public static List<(int VendorId, int ProductId)> GetWiredPidsForModel(string modelName)
    {
        var result = new List<(int, int)>();
        foreach (var kvp in Devices)
        {
            if (kvp.Value.ModelName == modelName && !kvp.Value.IsWireless)
                result.Add(kvp.Key);
        }
        return result;
    }
}
