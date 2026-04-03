using FluentAssertions;
using GBM.Core.Services;
using Xunit;

namespace GBM.Tests.Services;

public class CandidateGAdaptiveStrategyTests
{
    [Fact]
    public void BuildPlan_PrefersRecentSuccessfulPayloadPath()
    {
        var strategy = new CandidateGAdaptiveStrategy();
        var now = DateTime.UtcNow;

        strategy.RecordSuccess(CandidateGAttemptKind.Payload1, now);

        var plan = strategy.BuildPlan(now.AddSeconds(5));

        plan.Attempts.Should().NotBeEmpty();
        plan.Attempts[0].Should().Be(CandidateGAttemptKind.Payload1);
    }

    [Fact]
    public void BuildPlan_HasBoundedRetryBudget()
    {
        var strategy = new CandidateGAdaptiveStrategy();

        var plan = strategy.BuildPlan(DateTime.UtcNow);

        plan.Attempts.Count.Should().BeLessOrEqualTo(4);
    }

    [Fact]
    public void FailFailSuccessCadence_ResetsFailureStreakAndKeepsPreferredPath()
    {
        var strategy = new CandidateGAdaptiveStrategy();
        var now = DateTime.UtcNow;

        strategy.RecordFailure(now);
        strategy.RecordFailure(now.AddSeconds(1));
        strategy.FailureStreak.Should().Be(2);

        strategy.RecordSuccess(CandidateGAttemptKind.Payload0, now.AddSeconds(2));
        strategy.FailureStreak.Should().Be(0);

        var plan = strategy.BuildPlan(now.AddSeconds(3));
        plan.Attempts[0].Should().Be(CandidateGAttemptKind.Payload0);
    }

    [Fact]
    public void BuildPlan_DeprioritizesPrimer_WhenPrimerRecentlySucceeded()
    {
        var strategy = new CandidateGAdaptiveStrategy();
        var now = DateTime.UtcNow;

        strategy.RecordSuccess(CandidateGAttemptKind.Primer, now);

        var plan = strategy.BuildPlan(now.AddSeconds(10));

        plan.Attempts.Should().NotBeEmpty();
        plan.Attempts[0].Should().NotBe(CandidateGAttemptKind.Primer);
        plan.PrimerAttempts.Should().Be(1);
    }
}
