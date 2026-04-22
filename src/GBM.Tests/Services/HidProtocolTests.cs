using FluentAssertions;
using GBM.Core.Helpers;
using GBM.Core.Models;
using Xunit;

namespace GBM.Tests.Services;

public class HidProtocolTests
{
    [Fact]
    public void DeviceDatabase_KnownDevice_ReturnsCorrectModel()
    {
        var result = DeviceDatabase.TryGetDevice(0x258A, 0x2012, out var name, out var wireless);
        result.Should().BeTrue();
        name.Should().Be("Model D");
        wireless.Should().BeFalse();
    }

    [Fact]
    public void DeviceDatabase_KnownWirelessDevice_ReturnsWireless()
    {
        var result = DeviceDatabase.TryGetDevice(0x258A, 0x2023, out var name, out var wireless);
        result.Should().BeTrue();
        name.Should().Be("Model D");
        wireless.Should().BeTrue();
    }

    [Fact]
    public void DeviceDatabase_UnknownDevice_ReturnsFalse()
    {
        var result = DeviceDatabase.TryGetDevice(0x258A, 0xFFFF, out var name, out var wireless);
        result.Should().BeFalse();
        name.Should().Be("Unknown");
    }

    [Fact]
    public void DeviceDatabase_AlternateVendor_Works()
    {
        var result = DeviceDatabase.TryGetDevice(0x093A, 0x824D, out var name, out var wireless);
        result.Should().BeTrue();
        name.Should().Be("Model D2");
        wireless.Should().BeTrue();
    }

    [Fact]
    public void DeviceDatabase_ModelI2_PrimaryVendorMappings_AreCorrect()
    {
        var wired = DeviceDatabase.TryGetDevice(0x258A, 0x2014, out var wiredName, out var wiredWireless);
        wired.Should().BeTrue();
        wiredName.Should().Be("Model I2");
        wiredWireless.Should().BeFalse();

        var wireless = DeviceDatabase.TryGetDevice(0x258A, 0x2016, out var wirelessName, out var isWireless);
        wireless.Should().BeTrue();
        wirelessName.Should().Be("Model I2");
        isWireless.Should().BeTrue();
    }

    [Fact]
    public void DeviceDatabase_ModelI2_AlternateVendorMappings_AreCorrect()
    {
        var wired = DeviceDatabase.TryGetDevice(0x093A, 0x821A, out var wiredName, out var wiredWireless);
        wired.Should().BeTrue();
        wiredName.Should().Be("Model I2");
        wiredWireless.Should().BeFalse();

        var wireless = DeviceDatabase.TryGetDevice(0x093A, 0x821D, out var wirelessName, out var isWireless);
        wireless.Should().BeTrue();
        wirelessName.Should().Be("Model I2");
        isWireless.Should().BeTrue();
    }

    [Fact]
    public void DeviceDatabase_ModelI2_WiredPidSet_IncludesBothKnownWiredVariants()
    {
        var wiredPids = DeviceDatabase.GetWiredPidsForModel("Model I2");

        wiredPids.Should().Contain((0x258A, 0x2014));
        wiredPids.Should().Contain((0x093A, 0x821A));
    }

    [Fact]
    public void DeviceDatabase_IsKnownVendor_RecognizesPrimaryVendor()
    {
        DeviceDatabase.IsKnownVendor(0x258A).Should().BeTrue();
    }

    [Fact]
    public void DeviceDatabase_IsKnownVendor_RecognizesAlternateVendor()
    {
        DeviceDatabase.IsKnownVendor(0x093A).Should().BeTrue();
    }

    [Fact]
    public void DeviceDatabase_IsKnownVendor_RejectsUnknownVendor()
    {
        DeviceDatabase.IsKnownVendor(0x1234).Should().BeFalse();
    }

    [Fact]
    public void DeviceDatabase_AllModelsPresent()
    {
        var devices = DeviceDatabase.GetAllDevices();
        devices.Count.Should().BeGreaterThanOrEqualTo(19); // At least 19 entries
    }

    [Fact]
    public void BatteryState_DeriveHealth_Good()
    {
        BatteryState.DeriveHealth(50, 10).Should().Be(BatteryHealth.Good);
        BatteryState.DeriveHealth(100, 10).Should().Be(BatteryHealth.Good);
    }

    [Fact]
    public void BatteryState_DeriveHealth_Fair()
    {
        BatteryState.DeriveHealth(49, 10).Should().Be(BatteryHealth.Fair);
        BatteryState.DeriveHealth(20, 10).Should().Be(BatteryHealth.Fair);
    }

    [Fact]
    public void BatteryState_DeriveHealth_Low()
    {
        BatteryState.DeriveHealth(19, 10).Should().Be(BatteryHealth.Low);
        BatteryState.DeriveHealth(10, 10).Should().Be(BatteryHealth.Low);
    }

    [Fact]
    public void BatteryState_DeriveHealth_Critical()
    {
        BatteryState.DeriveHealth(9, 10).Should().Be(BatteryHealth.Critical);
        BatteryState.DeriveHealth(0, 10).Should().Be(BatteryHealth.Critical);
    }

    [Fact]
    public void BatteryState_Disconnected_HasDefaultValues()
    {
        var state = BatteryState.Disconnected;
        state.Level.Should().Be(0);
        state.IsCharging.Should().BeFalse();
        state.Connection.Should().Be(ConnectionState.NotConnected);
    }

    [Fact]
    public void DeviceInfo_CompositeKey_Format()
    {
        var info = new DeviceInfo
        {
            VendorId = 0x258A,
            ProductId = 0x2012,
            ReleaseNumber = 1,
            IsWireless = false
        };
        info.CompositeKey.Should().Be("258A_2012_1_False");
    }

    [Fact]
    public void BatteryColorHelper_Green_WhenHighLevel()
    {
        var color = BatteryColorHelper.GetColorHex(75, false, true);
        color.Should().Be("#22c55e");
    }

    [Fact]
    public void BatteryColorHelper_Amber_WhenMediumLevel()
    {
        var color = BatteryColorHelper.GetColorHex(30, false, true);
        color.Should().Be("#f59e0b");
    }

    [Fact]
    public void BatteryColorHelper_Red_WhenLowLevel()
    {
        var color = BatteryColorHelper.GetColorHex(10, false, true);
        color.Should().Be("#ef4444");
    }

    [Fact]
    public void BatteryColorHelper_Purple_WhenCharging()
    {
        var color = BatteryColorHelper.GetColorHex(50, true, true);
        color.Should().Be("#8b5cf6");
    }

    [Fact]
    public void BatteryColorHelper_Gray_WhenDisconnected()
    {
        var color = BatteryColorHelper.GetColorHex(50, false, false);
        color.Should().Be("#6b7280");
    }
}
