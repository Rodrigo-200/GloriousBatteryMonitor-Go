//go:build windows
// +build windows

package main

import (
    "fmt"
    "math"
    "sort"
    "sync"
    "time"
)

type BatteryPhase string

const (
    PhaseDischarge BatteryPhase = "discharge"
    PhaseCharge    BatteryPhase = "charge"
)

type DeviceKey struct {
    VendorID  uint16 `json:"vendorId"`
    ProductID uint16 `json:"productId"`
    Release   uint16 `json:"release"`
    Wireless  bool   `json:"wireless"`
}

func (k DeviceKey) String() string {
    wireless := "0"
    if k.Wireless {
        wireless = "1"
    }
    return fmt.Sprintf("%04x:%04x:%04x:%s", k.VendorID, k.ProductID, k.Release, wireless)
}

type BatteryEstimate struct {
    Valid       bool
    Hours       float64
    Lower       float64
    Upper       float64
    Confidence  float64
    Samples     int
    Phase       BatteryPhase
    Mode        string
    Paused      bool
    GeneratedAt time.Time
    RawRate     float64
}

type BatteryEstimator struct {
    mu      sync.Mutex
    devices map[DeviceKey]*deviceModel
}

func NewBatteryEstimator() *BatteryEstimator {
    return &BatteryEstimator{devices: make(map[DeviceKey]*deviceModel)}
}

func (b *BatteryEstimator) RecordSample(key DeviceKey, level int, charging bool, ts time.Time) BatteryEstimate {
    b.mu.Lock()
    defer b.mu.Unlock()

    model := b.ensureModel(key)
    phase := PhaseDischarge
    if charging {
        phase = PhaseCharge
    }
    est := model.recordSample(phase, level, ts)
    model.setLastEstimate(phase, est)
    return est
}

func (b *BatteryEstimator) GetLastEstimate(key DeviceKey, charging bool) (BatteryEstimate, bool) {
    b.mu.Lock()
    defer b.mu.Unlock()

    model, ok := b.devices[key]
    if !ok {
        return BatteryEstimate{}, false
    }
    phase := PhaseDischarge
    if charging {
        phase = PhaseCharge
    }
    est, ok := model.lastEstimate[phase]
    if !ok {
        return BatteryEstimate{}, false
    }
    return est, true
}

func (b *BatteryEstimator) Snapshot() map[string]PersistedDeviceModel {
    b.mu.Lock()
    defer b.mu.Unlock()

    snapshot := make(map[string]PersistedDeviceModel, len(b.devices))
    for key, model := range b.devices {
        snapshot[key.String()] = model.snapshot(key)
    }
    return snapshot
}

func (b *BatteryEstimator) Restore(models map[string]PersistedDeviceModel) {
    if len(models) == 0 {
        return
    }

    b.mu.Lock()
    defer b.mu.Unlock()

    for _, pm := range models {
        key := DeviceKey{
            VendorID:  pm.VendorID,
            ProductID: pm.ProductID,
            Release:   pm.Release,
            Wireless:  pm.Wireless,
        }
        model := newDeviceModel()
        model.discharge.restore(pm.Discharge)
        model.charge.restore(pm.Charge)
        b.devices[key] = model
    }
}

func (b *BatteryEstimator) Reset(key DeviceKey) {
    b.mu.Lock()
    defer b.mu.Unlock()
    delete(b.devices, key)
}

func (b *BatteryEstimator) ensureModel(key DeviceKey) *deviceModel {
    if model, ok := b.devices[key]; ok {
        return model
    }
    model := newDeviceModel()
    b.devices[key] = model
    return model
}

type deviceModel struct {
    discharge    *phaseEstimator
    charge       *phaseEstimator
    lastSample   map[BatteryPhase]phaseSample
    lastEstimate map[BatteryPhase]BatteryEstimate
}

func newDeviceModel() *deviceModel {
    return &deviceModel{
        discharge:    newPhaseEstimator(),
        charge:       newPhaseEstimator(),
        lastSample:   make(map[BatteryPhase]phaseSample),
        lastEstimate: make(map[BatteryPhase]BatteryEstimate),
    }
}

func (m *deviceModel) estimatorFor(phase BatteryPhase) *phaseEstimator {
    if phase == PhaseCharge {
        return m.charge
    }
    return m.discharge
}

func (m *deviceModel) setLastEstimate(phase BatteryPhase, est BatteryEstimate) {
    m.lastEstimate[phase] = est
}

func (m *deviceModel) recordSample(phase BatteryPhase, level int, ts time.Time) BatteryEstimate {
    estimator := m.estimatorFor(phase)
    estimator.markSeen(ts)

    // Ignore invalid levels
    if level < 0 || level > 100 {
        return m.buildEstimate(phase, level, ts)
    }

    // 0% and 100% plateaus should pause estimations but update last sample
    if level == 0 || level == 100 {
        m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
        estimator.markPlateau(ts)
        return m.buildEstimate(phase, level, ts)
    }

    last := m.lastSample[phase]
    if last.Timestamp.IsZero() {
        m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
        estimator.markFlat(ts)
        return m.buildEstimate(phase, level, ts)
    }

    deltaLevel := float64(last.Level - level)
    if phase == PhaseCharge {
        deltaLevel = float64(level - last.Level)
    }
    elapsed := ts.Sub(last.Timestamp)
    hours := elapsed.Hours()

    if hours <= 0 {
        estimator.markFlat(ts)
        m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
        return m.buildEstimate(phase, level, ts)
    }

    if deltaLevel <= 0 {
        estimator.markFlat(ts)
        m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
        return m.buildEstimate(phase, level, ts)
    }

    if deltaLevel < 0.8 {
        estimator.markFlat(ts)
        m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
        return m.buildEstimate(phase, level, ts)
    }

    slope := deltaLevel / hours
    if slope <= 0 || slope > 150 { // Guard unrealistic slopes
        estimator.markOutlier(ts)
        m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
        return m.buildEstimate(phase, level, ts)
    }

    if estimator.hasMedian() {
        median := estimator.median()
        if median > 0.05 {
            ratio := slope / median
            if ratio > 3.2 || ratio < 0.25 {
                estimator.markOutlier(ts)
                m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
                return m.buildEstimate(phase, level, ts)
            }
        }
    }

    estimator.addSlope(slope, ts)
    m.lastSample[phase] = phaseSample{Level: level, Timestamp: ts}
    return m.buildEstimate(phase, level, ts)
}

func (m *deviceModel) buildEstimate(phase BatteryPhase, level int, ts time.Time) BatteryEstimate {
    estimator := m.estimatorFor(phase)
    est := BatteryEstimate{
        Phase:       phase,
        Samples:     estimator.acceptedCount,
        GeneratedAt: ts,
        Confidence:  estimator.confidence(ts),
        RawRate:     estimator.finalRate(),
    }

    rate := est.RawRate
    if rate <= 0 {
        est.Paused = true
        est.Valid = false
        return est
    }

    switch phase {
    case PhaseDischarge:
        if level <= 0 {
            est.Paused = true
            est.Valid = false
            return est
        }
        rawHours := float64(level) / rate
        est.Hours = applyDischargeCurve(rawHours, level)
        est.Mode = "discharge"
    case PhaseCharge:
        remaining := 100 - level
        if remaining <= 0 {
            est.Paused = true
            est.Valid = false
            return est
        }
        rawHours := float64(remaining) / rate
        est.Hours = applyChargeCurve(rawHours, remaining, rate)
        if rate < 6 {
            est.Mode = "trickle"
        } else {
            est.Mode = "fast"
        }
    }

    est.Hours = clampFloat(est.Hours, 0.05, 200)

    if estimator.isStale(ts) || est.Confidence < 0.12 {
        est.Paused = true
        est.Valid = false
    } else {
        est.Valid = true
    }

    spread := computeSpread(est.Confidence)
    est.Lower = clampFloat(est.Hours*(1-spread), 0.02, est.Hours)
    est.Upper = clampFloat(est.Hours*(1+spread), est.Hours, 240)

    return est
}

type phaseSample struct {
    Level     int
    Timestamp time.Time
}

type phaseEstimator struct {
    ema           float64
    recent        []float64
    sampleCount   int
    acceptedCount int
    lastAccepted  time.Time
    lastSeen      time.Time
    flatCount     int
    flatSince     time.Time
}

func newPhaseEstimator() *phaseEstimator { return &phaseEstimator{recent: make([]float64, 0, maxRecentWindow)} }

const (
    maxRecentWindow       = 12
    staleAfter             = 8 * time.Minute
    plateauPauseAfter      = 3 * time.Minute
    minConfidenceSamples   = 8.0
    epsilon                = 1e-6
)

func (p *phaseEstimator) markSeen(ts time.Time) { p.lastSeen = ts }

func (p *phaseEstimator) addSlope(slope float64, ts time.Time) {
    if math.IsInf(slope, 0) || math.IsNaN(slope) {
        return
    }
    p.sampleCount++
    p.acceptedCount++
    if len(p.recent) >= maxRecentWindow {
        copy(p.recent, p.recent[1:])
        p.recent[len(p.recent)-1] = slope
    } else {
        p.recent = append(p.recent, slope)
    }
    if p.acceptedCount == 1 {
        p.ema = slope
    } else {
        alpha := 0.35
        if p.acceptedCount <= 3 {
            alpha = 0.55
        } else if p.acceptedCount >= 8 {
            alpha = 0.22
        }
        p.ema = alpha*slope + (1-alpha)*p.ema
    }
    p.lastAccepted = ts
    p.flatCount = 0
    p.flatSince = time.Time{}
}

func (p *phaseEstimator) markFlat(ts time.Time) {
    p.sampleCount++
    p.flatCount++
    if p.flatSince.IsZero() {
        p.flatSince = ts
    }
}

func (p *phaseEstimator) markPlateau(ts time.Time) {
    p.markFlat(ts)
    p.flatCount += 2
}

func (p *phaseEstimator) markOutlier(ts time.Time) {
    p.sampleCount++
    if p.flatSince.IsZero() {
        p.flatSince = ts
    }
}

func (p *phaseEstimator) hasMedian() bool { return len(p.recent) >= 3 }

func (p *phaseEstimator) median() float64 {
    if len(p.recent) == 0 {
        return 0
    }
    sorted := append([]float64(nil), p.recent...)
    sort.Float64s(sorted)
    mid := len(sorted) / 2
    if len(sorted)%2 == 0 {
        return (sorted[mid-1] + sorted[mid]) / 2
    }
    return sorted[mid]
}

func (p *phaseEstimator) finalRate() float64 {
    if p.acceptedCount == 0 {
        return 0
    }
    med := p.median()
    if p.acceptedCount == 1 {
        if med == 0 {
            return p.ema
        }
        return med
    }
    if med == 0 {
        return p.ema
    }
    if p.acceptedCount == 2 {
        return (p.ema + med) / 2
    }
    return 0.55*p.ema + 0.45*med
}

func (p *phaseEstimator) confidence(now time.Time) float64 {
    if p.acceptedCount == 0 {
        return 0
    }
    sampleScore := math.Min(1, float64(p.acceptedCount)/minConfidenceSamples)

    stability := 1.0
    if len(p.recent) >= 3 {
        med := p.median()
        if med > epsilon {
            deviations := make([]float64, len(p.recent))
            for i, v := range p.recent {
                deviations[i] = math.Abs(v - med)
            }
            mad := medianFloat64(deviations)
            ratio := mad / med
            if ratio > 1 {
                ratio = 1
            }
            stability = 1 - ratio
        }
    }

    recency := 1.0
    if !p.lastAccepted.IsZero() {
        age := now.Sub(p.lastAccepted)
        switch {
        case age > 12*time.Minute:
            recency = 0.2
        case age > 8*time.Minute:
            recency = 0.4
        case age > 5*time.Minute:
            recency = 0.6
        case age > 3*time.Minute:
            recency = 0.8
        }
    }

    conf := sampleScore * stability * recency
    return clampFloat(conf, 0, 1)
}

func (p *phaseEstimator) isStale(now time.Time) bool {
    if p.acceptedCount == 0 {
        if p.flatSince.IsZero() {
            return true
        }
        return now.Sub(p.flatSince) > plateauPauseAfter
    }
    if p.lastAccepted.IsZero() {
        return true
    }
    return now.Sub(p.lastAccepted) > staleAfter
}

func medianFloat64(values []float64) float64 {
    if len(values) == 0 {
        return 0
    }
    sorted := append([]float64(nil), values...)
    sort.Float64s(sorted)
    mid := len(sorted) / 2
    if len(sorted)%2 == 0 {
        return (sorted[mid-1] + sorted[mid]) / 2
    }
    return sorted[mid]
}

func clampFloat(v, min, max float64) float64 {
    if v < min {
        return min
    }
    if v > max {
        return max
    }
    return v
}

func computeSpread(confidence float64) float64 {
    if confidence >= 0.85 {
        return 0.15
    }
    if confidence >= 0.65 {
        return 0.22
    }
    if confidence >= 0.4 {
        return 0.32
    }
    spread := 0.45 + (0.4*(0.4-confidence))
    if spread > 0.6 {
        spread = 0.6
    }
    return spread
}

type curvePoint struct {
    pct    int
    factor float64
}

var dischargeCurve = []curvePoint{
    {pct: 0, factor: 1.35},
    {pct: 5, factor: 1.28},
    {pct: 10, factor: 1.2},
    {pct: 15, factor: 1.15},
    {pct: 20, factor: 1.1},
    {pct: 40, factor: 1.0},
    {pct: 60, factor: 0.95},
    {pct: 80, factor: 0.88},
    {pct: 90, factor: 0.76},
    {pct: 95, factor: 0.68},
    {pct: 100, factor: 0.6},
}

var chargeCurve = []curvePoint{
    {pct: 0, factor: 0.85},
    {pct: 10, factor: 0.92},
    {pct: 20, factor: 1.0},
    {pct: 40, factor: 1.05},
    {pct: 60, factor: 1.1},
    {pct: 80, factor: 1.18},
    {pct: 90, factor: 1.28},
    {pct: 95, factor: 1.4},
    {pct: 100, factor: 1.55},
}

func applyDischargeCurve(hours float64, level int) float64 {
    factor := interpolateCurve(dischargeCurve, level)
    return hours * factor
}

func applyChargeCurve(hours float64, remaining int, rate float64) float64 {
    factor := interpolateCurve(chargeCurve, remaining)
    if rate < 6 {
        factor *= 1.15
    } else if rate > 14 {
        factor *= 0.95
    }
    return hours * factor
}

func interpolateCurve(points []curvePoint, pct int) float64 {
    if len(points) == 0 {
        return 1
    }
    if pct <= points[0].pct {
        return points[0].factor
    }
    if pct >= points[len(points)-1].pct {
        return points[len(points)-1].factor
    }
    for i := 0; i < len(points)-1; i++ {
        a := points[i]
        b := points[i+1]
        if pct >= a.pct && pct <= b.pct {
            span := float64(b.pct - a.pct)
            if span <= 0 {
                return b.factor
            }
            t := float64(pct-a.pct) / span
            return a.factor + t*(b.factor-a.factor)
        }
    }
    return points[len(points)-1].factor
}

// PersistedDeviceModel represents the on-disk snapshot of a device estimator.
type PersistedDeviceModel struct {
    VendorID  uint16                 `json:"vendorId"`
    ProductID uint16                 `json:"productId"`
    Release   uint16                 `json:"release"`
    Wireless  bool                   `json:"wireless"`
    Discharge PersistedPhaseEstimator `json:"discharge"`
    Charge    PersistedPhaseEstimator `json:"charge"`
}

type PersistedPhaseEstimator struct {
    EMA          float64   `json:"ema"`
    Recent       []float64 `json:"recent"`
    SampleCount  int       `json:"sampleCount"`
    Accepted     int       `json:"accepted"`
    LastAccepted string    `json:"lastAccepted,omitempty"`
    FlatCount    int       `json:"flatCount,omitempty"`
    FlatSince    string    `json:"flatSince,omitempty"`
}

func (m *deviceModel) snapshot(key DeviceKey) PersistedDeviceModel {
    return PersistedDeviceModel{
        VendorID:  key.VendorID,
        ProductID: key.ProductID,
        Release:   key.Release,
        Wireless:  key.Wireless,
        Discharge: m.discharge.snapshot(),
        Charge:    m.charge.snapshot(),
    }
}

func (p *phaseEstimator) snapshot() PersistedPhaseEstimator {
    copyLen := len(p.recent)
    if copyLen > maxRecentWindow {
        copyLen = maxRecentWindow
    }
    recent := make([]float64, copyLen)
    copy(recent, p.recent[:copyLen])
    snap := PersistedPhaseEstimator{
        EMA:         p.ema,
        Recent:      recent,
        SampleCount: p.sampleCount,
        Accepted:    p.acceptedCount,
        FlatCount:   p.flatCount,
    }
    if !p.lastAccepted.IsZero() {
        snap.LastAccepted = p.lastAccepted.Format(time.RFC3339)
    }
    if !p.flatSince.IsZero() {
        snap.FlatSince = p.flatSince.Format(time.RFC3339)
    }
    return snap
}

func (p *phaseEstimator) restore(data PersistedPhaseEstimator) {
    p.ema = data.EMA
    p.sampleCount = data.SampleCount
    p.acceptedCount = data.Accepted
    p.recent = append([]float64(nil), data.Recent...)
    if len(p.recent) > maxRecentWindow {
        p.recent = p.recent[len(p.recent)-maxRecentWindow:]
    }
    if data.LastAccepted != "" {
        if t, err := time.Parse(time.RFC3339, data.LastAccepted); err == nil {
            p.lastAccepted = t
        }
    }
    if data.FlatSince != "" {
        if t, err := time.Parse(time.RFC3339, data.FlatSince); err == nil {
            p.flatSince = t
        }
    }
    p.flatCount = data.FlatCount
}
