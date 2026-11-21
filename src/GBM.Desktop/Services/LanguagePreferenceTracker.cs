namespace GBM.Desktop.Services;

/// <summary>
/// Tracks the persisted language preference separately from the live preview.
/// Saves unrelated settings (such as window placement) must not reset that preview.
/// </summary>
internal sealed class LanguagePreferenceTracker
{
    private string _preference;

    public LanguagePreferenceTracker(string? initialPreference)
    {
        _preference = Localization.Normalize(initialPreference);
    }

    public bool HasChanged(string? preference)
    {
        var normalized = Localization.Normalize(preference);
        if (string.Equals(_preference, normalized, StringComparison.Ordinal))
            return false;

        _preference = normalized;
        return true;
    }
}
