package main_test

import (
	"testing"

	"github.com/dop251/goja"
)

const gaugeScript = `
const GAUGE_BADGE_BASE = 'px-4 py-2 rounded-full text-sm font-semibold';
const BADGE_VARIANTS = {
    neutral: GAUGE_BADGE_BASE + ' bg-gray-200 dark:bg-gray-800 text-gray-600 dark:text-gray-400',
    good: GAUGE_BADGE_BASE + ' bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-300',
    warning: GAUGE_BADGE_BASE + ' bg-yellow-100 dark:bg-yellow-900/30 text-yellow-700 dark:text-yellow-300',
    danger: GAUGE_BADGE_BASE + ' bg-red-100 dark:bg-red-900/30 text-red-700 dark:text-red-300',
    info: GAUGE_BADGE_BASE + ' bg-blue-100 dark:bg-blue-900/30 text-blue-700 dark:text-blue-300',
};

const ICON_BATTERY = '<svg class="w-12 h-12 text-fluent-text dark:text-fluent-dark-text" fill="none" stroke="currentColor" viewBox="0 0 24 24">\n                    <rect x="2" y="7" width="18" height="11" rx="2"/>\n                    <path d="M22 10v4"/>\n                </svg>';
const ICON_BATTERY_LOW = '<svg class="w-12 h-12 text-fluent-text dark:text-fluent-dark-text" fill="none" stroke="currentColor" viewBox="0 0 24 24">\n                    <rect x="2" y="7" width="18" height="11" rx="2"/>\n                    <path d="M22 10v4"/>\n                    <line x1="6" y1="12" x2="10" y2="12" stroke-width="3"/>\n                </svg>';
const ICON_CHARGING = '<svg class="w-12 h-12 text-blue-500 dark:text-blue-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">\n                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>\n                </svg>';
const ICON_DISCONNECTED = '<svg class="w-12 h-12 text-gray-400 dark:text-gray-600" fill="none" stroke="currentColor" viewBox="0 0 24 24">\n                    <circle cx="12" cy="12" r="10"/>\n                    <line x1="8" y1="12" x2="16" y2="12"/>\n                </svg>';

function clamp(n, min, max) {
    return Math.max(min, Math.min(max, n));
}

function deriveGaugeVisual(state, previous) {
    if (!previous) {
        previous = {};
    }
    const level = typeof state.level === 'number' ? clamp(state.level, 0, 100) : null;
    const connected = !!state.connected;
    const charging = !!state.charging;
    const lastKnown = !!state.lastKnown;
    const reading = !!state.reading;

    if (reading) {
        const levelText = level !== null ? level + '%' : 'Reading...';
        return {
            levelText: levelText,
            statusText: 'Reading...',
            badgeClass: BADGE_VARIANTS.neutral,
            ringClass: previous.ringClass || 'battery-ring text-gray-400 dark:text-gray-600',
            iconMarkup: previous.iconMarkup || ICON_BATTERY,
        };
    }

    if (!connected) {
        if (lastKnown && level !== null) {
            return {
                levelText: level + '%',
                statusText: 'Last Known',
                badgeClass: BADGE_VARIANTS.neutral,
                ringClass: 'battery-ring text-gray-400 dark:text-gray-600',
                iconMarkup: ICON_DISCONNECTED,
            };
        }
        return {
            levelText: '--',
            statusText: 'Not Connected',
            badgeClass: BADGE_VARIANTS.neutral,
            ringClass: 'battery-ring text-gray-400 dark:text-gray-600',
            iconMarkup: ICON_DISCONNECTED,
        };
    }

    const levelText = level !== null ? level + '%' : '--';

    if (charging) {
        return {
            levelText: levelText,
            statusText: 'Charging',
            badgeClass: BADGE_VARIANTS.info,
            ringClass: 'battery-ring text-blue-500 dark:text-blue-400',
            iconMarkup: ICON_CHARGING,
        };
    }

    if (level === null) {
        return {
            levelText: levelText,
            statusText: 'Good',
            badgeClass: BADGE_VARIANTS.good,
            ringClass: 'battery-ring text-green-500 dark:text-green-400',
            iconMarkup: ICON_BATTERY,
        };
    }

    if (level >= 50) {
        return {
            levelText: levelText,
            statusText: 'Good',
            badgeClass: BADGE_VARIANTS.good,
            ringClass: 'battery-ring text-green-500 dark:text-green-400',
            iconMarkup: ICON_BATTERY,
        };
    }
    if (level >= 20) {
        return {
            levelText: levelText,
            statusText: 'Fair',
            badgeClass: BADGE_VARIANTS.warning,
            ringClass: 'battery-ring text-yellow-500 dark:text-yellow-400',
            iconMarkup: ICON_BATTERY,
        };
    }
    return {
        levelText: levelText,
        statusText: 'Low',
        badgeClass: BADGE_VARIANTS.danger,
        ringClass: 'battery-ring text-red-500 dark:text-red-400',
        iconMarkup: ICON_BATTERY_LOW,
    };
}
`

func runGaugeVisual(t *testing.T, vm *goja.Runtime, state, previous map[string]interface{}) map[string]interface{} {
	t.Helper()
	var (
		res goja.Value
		err error
	)

	if previous == nil {
		res, err = vm.Call("deriveGaugeVisual", vm.GlobalObject(), state)
	} else {
		res, err = vm.Call("deriveGaugeVisual", vm.GlobalObject(), state, previous)
	}
	if err != nil {
		t.Fatalf("deriveGaugeVisual error: %v", err)
	}

	exported := res.Export()
	out, ok := exported.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type %T", exported)
	}
	return out
}

func mustString(t *testing.T, value interface{}, key string) string {
	t.Helper()
	str, ok := value.(string)
	if !ok {
		t.Fatalf("expected string for %s, got %T", key, value)
	}
	return str
}

func TestDeriveGaugeVisual(t *testing.T) {
	vm := goja.New()
	if _, err := vm.RunString(gaugeScript); err != nil {
		t.Fatalf("unable to evaluate gauge script: %v", err)
	}

	badgesObj := vm.Get("BADGE_VARIANTS").ToObject(vm)
	badgeNeutral := badgesObj.Get("neutral").String()
	badgeGood := badgesObj.Get("good").String()
	badgeWarning := badgesObj.Get("warning").String()
	badgeDanger := badgesObj.Get("danger").String()
	badgeInfo := badgesObj.Get("info").String()

	iconBattery := vm.Get("ICON_BATTERY").String()
	iconBatteryLow := vm.Get("ICON_BATTERY_LOW").String()
	iconCharging := vm.Get("ICON_CHARGING").String()
	iconDisconnected := vm.Get("ICON_DISCONNECTED").String()

	tests := []struct {
		name     string
		state    map[string]interface{}
		previous map[string]interface{}
		want     map[string]string
	}{
		{
			name:  "connected good",
			state: map[string]interface{}{"connected": true, "level": 82},
			want: map[string]string{
				"statusText": "Good",
				"badgeClass": badgeGood,
				"ringClass":  "battery-ring text-green-500 dark:text-green-400",
				"iconMarkup": iconBattery,
				"levelText":  "82%",
			},
		},
		{
			name:  "connected fair",
			state: map[string]interface{}{"connected": true, "level": 35},
			want: map[string]string{
				"statusText": "Fair",
				"badgeClass": badgeWarning,
				"ringClass":  "battery-ring text-yellow-500 dark:text-yellow-400",
				"iconMarkup": iconBattery,
				"levelText":  "35%",
			},
		},
		{
			name:  "connected low",
			state: map[string]interface{}{"connected": true, "level": 12},
			want: map[string]string{
				"statusText": "Low",
				"badgeClass": badgeDanger,
				"ringClass":  "battery-ring text-red-500 dark:text-red-400",
				"iconMarkup": iconBatteryLow,
				"levelText":  "12%",
			},
		},
		{
			name:  "charging",
			state: map[string]interface{}{"connected": true, "charging": true, "level": 48},
			want: map[string]string{
				"statusText": "Charging",
				"badgeClass": badgeInfo,
				"ringClass":  "battery-ring text-blue-500 dark:text-blue-400",
				"iconMarkup": iconCharging,
				"levelText":  "48%",
			},
		},
		{
			name:  "disconnected last known",
			state: map[string]interface{}{"connected": false, "lastKnown": true, "level": 57},
			want: map[string]string{
				"statusText": "Last Known",
				"badgeClass": badgeNeutral,
				"ringClass":  "battery-ring text-gray-400 dark:text-gray-600",
				"iconMarkup": iconDisconnected,
				"levelText":  "57%",
			},
		},
		{
			name:  "disconnected no data",
			state: map[string]interface{}{"connected": false},
			want: map[string]string{
				"statusText": "Not Connected",
				"badgeClass": badgeNeutral,
				"ringClass":  "battery-ring text-gray-400 dark:text-gray-600",
				"iconMarkup": iconDisconnected,
				"levelText":  "--",
			},
		},
		{
			name:  "reading preserves visuals",
			state: map[string]interface{}{"reading": true, "level": 47},
			previous: map[string]interface{}{
				"ringClass":  "battery-ring text-yellow-500 dark:text-yellow-400",
				"iconMarkup": iconBattery,
			},
			want: map[string]string{
				"statusText": "Reading...",
				"badgeClass": badgeNeutral,
				"ringClass":  "battery-ring text-yellow-500 dark:text-yellow-400",
				"iconMarkup": iconBattery,
				"levelText":  "47%",
			},
		},
		{
			name:  "clamps above one hundred",
			state: map[string]interface{}{"connected": true, "level": 150},
			want: map[string]string{
				"statusText": "Good",
				"badgeClass": badgeGood,
				"ringClass":  "battery-ring text-green-500 dark:text-green-400",
				"iconMarkup": iconBattery,
				"levelText":  "100%",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := runGaugeVisual(t, vm, tt.state, tt.previous)
			for key, wantVal := range tt.want {
				got := mustString(t, result[key], key)
				if got != wantVal {
					t.Fatalf("%s mismatch: want %q, got %q", key, wantVal, got)
				}
			}
		})
	}
}

func TestDeriveGaugeVisualDisconnectedDefaults(t *testing.T) {
	vm := goja.New()
	if _, err := vm.RunString(gaugeScript); err != nil {
		t.Fatalf("unable to evaluate gauge script: %v", err)
	}

	result := runGaugeVisual(t, vm, map[string]interface{}{}, nil)
	if mustString(t, result["statusText"], "statusText") != "Not Connected" {
		t.Fatalf("expected Not Connected status, got %v", result["statusText"])
	}
	if mustString(t, result["levelText"], "levelText") != "--" {
		t.Fatalf("expected level text '--', got %v", result["levelText"])
	}
}
