using FluentAssertions;
using GBM.Desktop.Services;
using Xunit;

namespace GBM.Tests.Services;

public sealed class SingleInstanceCoordinatorTests
{
    [Fact]
    public async Task SecondInstance_NotifiesRegisteredPrimaryHandler()
    {
        var names = CreateUniqueNames();
        using var primary = SingleInstanceCoordinator.CreateForTesting(names.Mutex, names.Pipe);
        using var secondary = SingleInstanceCoordinator.CreateForTesting(names.Mutex, names.Pipe);
        var activated = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);

        primary.IsPrimaryInstance.Should().BeTrue();
        secondary.IsPrimaryInstance.Should().BeFalse();
        primary.RegisterActivationHandler(() => activated.TrySetResult());

        bool notified = await secondary.NotifyPrimaryInstanceAsync(TimeSpan.FromSeconds(5));

        notified.Should().BeTrue();
        await activated.Task.WaitAsync(TimeSpan.FromSeconds(5));
    }

    [Fact]
    public async Task ActivationBeforeHandlerRegistration_IsDeliveredWhenHandlerRegisters()
    {
        var names = CreateUniqueNames();
        using var primary = SingleInstanceCoordinator.CreateForTesting(names.Mutex, names.Pipe);
        using var secondary = SingleInstanceCoordinator.CreateForTesting(names.Mutex, names.Pipe);
        var activated = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);

        (await secondary.NotifyPrimaryInstanceAsync(TimeSpan.FromSeconds(5)))
            .Should().BeTrue();

        primary.RegisterActivationHandler(() => activated.TrySetResult());

        await activated.Task.WaitAsync(TimeSpan.FromSeconds(5));
    }

    [Fact]
    public async Task RepeatedLaunches_ReuseOnePrimaryAndDeliverEachActivation()
    {
        var names = CreateUniqueNames();
        using var primary = SingleInstanceCoordinator.CreateForTesting(names.Mutex, names.Pipe);
        using var secondary = SingleInstanceCoordinator.CreateForTesting(names.Mutex, names.Pipe);
        var activatedTwice = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        int activationCount = 0;

        primary.RegisterActivationHandler(() =>
        {
            if (Interlocked.Increment(ref activationCount) == 2)
                activatedTwice.TrySetResult();
        });

        for (int i = 0; i < 2; i++)
        {
            (await secondary.NotifyPrimaryInstanceAsync(TimeSpan.FromSeconds(5)))
                .Should().BeTrue();
        }

        await activatedTwice.Task.WaitAsync(TimeSpan.FromSeconds(5));
        activationCount.Should().Be(2);
    }

    private static (string Mutex, string Pipe) CreateUniqueNames()
    {
        string suffix = Guid.NewGuid().ToString("N");
        return ($"GBM.Tests.Mutex.{suffix}", $"GBM.Tests.Pipe.{suffix}");
    }
}
