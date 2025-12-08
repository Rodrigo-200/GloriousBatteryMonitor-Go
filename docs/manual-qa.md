# Manual QA Checklist – Battery Gauge Rendering

Use this checklist when validating changes to the main UI battery gauge.

- [ ] Launch the application with a Glorious mouse connected and confirm the large battery gauge immediately reflects the device percentage (the numeric label and ring fill should match the tray tooltip).
- [ ] Connect the charger to the mouse and observe the gauge transition to the charging style (blue ring, lightning icon, "Charging" badge) within one update tick.
- [ ] Drain or spoof the device to enter the low battery threshold (<20%) and verify the gauge switches to the low styling (red ring, low icon, "Low" badge) without stale values.
- [ ] Trigger a manual rescan/read cycle (for example by clicking refresh in the UI) and confirm the temporary "Reading..." state shows the last known level and restores to live telemetry once the read completes.
- [ ] Disconnect the mouse or put the PC to sleep/wake cycle; ensure the gauge falls back to "Last Known" when history is available and clears to "Not Connected" with an empty ring when no level can be resolved.
- [ ] Toggle between light and dark themes after each of the above states to confirm color tokens render correctly and the ring animation remains smooth after theme changes.
