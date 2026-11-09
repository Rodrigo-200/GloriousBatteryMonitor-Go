using FluentAssertions;
using GBM.Desktop.Services;
using Xunit;

namespace GBM.Tests.Services;

public class LanguagePreferenceTrackerTests
{
    [Fact]
    public void HasChanged_UnchangedPersistedLanguageDoesNotReapplyAndUndoPreview()
    {
        var tracker = new LanguagePreferenceTracker("ru");

        // Saving window position raises SettingsChanged with the still-saved preference.
        // A previewed language is live UI state and must not be reset by that save.
        tracker.HasChanged("ru").Should().BeFalse();

        // Saving the actual language selection should apply it once.
        tracker.HasChanged("en").Should().BeTrue();
        tracker.HasChanged("en").Should().BeFalse();
    }
}
