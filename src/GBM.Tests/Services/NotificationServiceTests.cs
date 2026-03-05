using FluentAssertions;
using GBM.Core.Models;
using GBM.Core.Services;
using Microsoft.Extensions.Logging;
using Moq;
using Xunit;

namespace GBM.Tests.Services;

public class NotificationServiceTests
{
    private readonly NotificationService _service;
    private readonly List<(NotificationType Type, string Title, string Message)> _notifications = new();

    public NotificationServiceTests()
    {
        var logger = new Mock<ILogger<NotificationService>>();
        _service = new NotificationService(logger.Object);
        _service.NotificationTriggered += (type, title, message) =>
            _notifications.Add((type, title, message));
    }

    [Fact]
    public void LowBattery_FiresWhenCrossingThreshold()
    {
        var settings = new AppSettings { NotificationsEnabled = true, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        var previous = new BatteryState { Level = 21, IsCharging = false, Connection = ConnectionState.Connected };
        var current = new BatteryState { Level = 19, IsCharging = false, Connection = ConnectionState.Connected };

        _service.ProcessBatteryUpdate(current, previous, settings);

        _notifications.Should().ContainSingle(n => n.Type == NotificationType.Low);
    }

    [Fact]
    public void LowBattery_DoesNotFireWhenAboveThreshold()
    {
        var settings = new AppSettings { NotificationsEnabled = true, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        var previous = new BatteryState { Level = 50, IsCharging = false, Connection = ConnectionState.Connected };
        var current = new BatteryState { Level = 45, IsCharging = false, Connection = ConnectionState.Connected };

        _service.ProcessBatteryUpdate(current, previous, settings);

        _notifications.Should().BeEmpty();
    }

    [Fact]
    public void CriticalBattery_FiresWhenCrossingThreshold()
    {
        var settings = new AppSettings { NotificationsEnabled = true, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        var previous = new BatteryState { Level = 11, IsCharging = false, Connection = ConnectionState.Connected };
        var current = new BatteryState { Level = 9, IsCharging = false, Connection = ConnectionState.Connected };

        _service.ProcessBatteryUpdate(current, previous, settings);

        _notifications.Should().Contain(n => n.Type == NotificationType.Critical);
    }

    [Fact]
    public void FullCharge_FiresAt100WhileCharging()
    {
        var settings = new AppSettings { NotificationsEnabled = true, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        var previous = new BatteryState { Level = 99, IsCharging = true, Connection = ConnectionState.Connected };
        var current = new BatteryState { Level = 100, IsCharging = true, Connection = ConnectionState.Connected };

        _service.ProcessBatteryUpdate(current, previous, settings);

        _notifications.Should().ContainSingle(n => n.Type == NotificationType.FullCharge);
    }

    [Fact]
    public void Disconnected_FiresOnConnectionLoss()
    {
        var settings = new AppSettings { NotificationsEnabled = true, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        var previous = new BatteryState { Level = 80, IsCharging = false, Connection = ConnectionState.Connected };
        var current = new BatteryState { Level = 80, IsCharging = false, Connection = ConnectionState.NotConnected };

        _service.ProcessBatteryUpdate(current, previous, settings);

        _notifications.Should().ContainSingle(n => n.Type == NotificationType.Disconnected);
    }

    [Fact]
    public void Notifications_DoNotFireWhenDisabled()
    {
        var settings = new AppSettings { NotificationsEnabled = false, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        var previous = new BatteryState { Level = 21, IsCharging = false, Connection = ConnectionState.Connected };
        var current = new BatteryState { Level = 5, IsCharging = false, Connection = ConnectionState.Connected };

        _service.ProcessBatteryUpdate(current, previous, settings);

        _notifications.Should().BeEmpty();
    }

    [Fact]
    public void ChargingStateChange_ResetsLowBatteryFlag()
    {
        var settings = new AppSettings { NotificationsEnabled = true, LowBatteryThreshold = 20, CriticalBatteryThreshold = 10 };

        // Cross low threshold
        var p1 = new BatteryState { Level = 21, IsCharging = false, Connection = ConnectionState.Connected };
        var c1 = new BatteryState { Level = 19, IsCharging = false, Connection = ConnectionState.Connected };
        _service.ProcessBatteryUpdate(c1, p1, settings);
        _notifications.Should().ContainSingle(n => n.Type == NotificationType.Low);
        _notifications.Clear();

        // Start charging (resets flags)
        var p2 = c1;
        var c2 = new BatteryState { Level = 19, IsCharging = true, Connection = ConnectionState.Connected };
        _service.ProcessBatteryUpdate(c2, p2, settings);
        _notifications.Clear();

        // Stop charging and cross threshold again
        var p3 = new BatteryState { Level = 21, IsCharging = false, Connection = ConnectionState.Connected };
        var c3 = new BatteryState { Level = 19, IsCharging = false, Connection = ConnectionState.Connected };
        _service.ProcessBatteryUpdate(c3, p3, settings);

        // Should fire again because flag was reset
        _notifications.Should().Contain(n => n.Type == NotificationType.Low);
    }
}
