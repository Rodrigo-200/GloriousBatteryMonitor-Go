//go:build windows
// +build windows

package main

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type HistorySample struct {
	Timestamp time.Time `json:"timestamp"`
	Level     int       `json:"level"`
	Charging  bool      `json:"charging"`
	Rate      float64   `json:"rate"`
}

type HistoryEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Level     int       `json:"level"`
	Charging  bool      `json:"charging"`
	Message   string    `json:"message,omitempty"`
}

type HistoryPointResponse struct {
	Ts       int64   `json:"ts"`
	Level    float64 `json:"level"`
	Charging bool    `json:"charging"`
	Rate     float64 `json:"rate"`
}

type HistoryEventResponse struct {
	Ts       int64  `json:"ts"`
	Type     string `json:"type"`
	Level    int    `json:"level"`
	Charging bool   `json:"charging"`
	Message  string `json:"message,omitempty"`
}

type HistoryResponse struct {
	Range          string                 `json:"range"`
	From           int64                  `json:"from"`
	To             int64                  `json:"to"`
	Points         []HistoryPointResponse `json:"points"`
	Events         []HistoryEventResponse `json:"events"`
	Version        uint64                 `json:"version"`
	LatestLevel    int                    `json:"latestLevel"`
	LatestCharging bool                   `json:"latestCharging"`
}

var (
	historyMu         sync.RWMutex
	historySamples    []HistorySample
	historyEvents     []HistoryEvent
	historyVersion    uint64
	lastHistoryStatus string
)

const (
	historyRetention        = 8 * 24 * time.Hour
	minHistorySpacing       = 75 * time.Second
	significantLevelDelta   = 3
	maxHistorySamples       = 7200
	maxHistoryEvents        = 256
	historyDefaultMaxPoints = 480
	maxRateMagnitude        = 50.0
)

func loadHistoryFromChargeData(samples []HistorySample, events []HistoryEvent) {
	now := time.Now()
	cutoff := now.Add(-historyRetention)

	historyMu.Lock()
	defer historyMu.Unlock()

	historySamples = historySamples[:0]
	for _, sample := range samples {
		if sample.Timestamp.IsZero() {
			continue
		}
		if sample.Timestamp.Before(cutoff) {
			continue
		}
		if sample.Level < 0 {
			sample.Level = 0
		}
		if sample.Level > 100 {
			sample.Level = 100
		}
		sample.Rate = clampRate(sample.Rate)
		historySamples = append(historySamples, sample)
	}
	sort.Slice(historySamples, func(i, j int) bool {
		return historySamples[i].Timestamp.Before(historySamples[j].Timestamp)
	})
	if len(historySamples) > maxHistorySamples {
		historySamples = decimateSamples(historySamples, maxHistorySamples)
	}

	historyEvents = historyEvents[:0]
	for _, evt := range events {
		if evt.Timestamp.IsZero() {
			continue
		}
		if evt.Timestamp.Before(cutoff) {
			continue
		}
		historyEvents = append(historyEvents, evt)
	}
	sort.Slice(historyEvents, func(i, j int) bool {
		return historyEvents[i].Timestamp.Before(historyEvents[j].Timestamp)
	})
	if len(historyEvents) > maxHistoryEvents {
		historyEvents = historyEvents[len(historyEvents)-maxHistoryEvents:]
	}

	lastHistoryStatus = ""
	atomic.StoreUint64(&historyVersion, uint64(len(historySamples)))
}

func recordHistorySample(level int, charging bool) {
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	now := time.Now()

	historyMu.Lock()
	var rate float64
	if len(historySamples) > 0 {
		last := historySamples[len(historySamples)-1]
		dt := now.Sub(last.Timestamp)
		if dt <= 0 {
			historyMu.Unlock()
			return
		}
		rate = float64(level-last.Level) / dt.Hours()
		if dt < minHistorySpacing && absInt(level-last.Level) < significantLevelDelta && charging == last.Charging {
			last.Timestamp = now
			last.Level = level
			last.Charging = charging
			last.Rate = clampRate(rate)
			historySamples[len(historySamples)-1] = last
			historyMu.Unlock()
			return
		}
	}

	sample := HistorySample{
		Timestamp: now,
		Level:     level,
		Charging:  charging,
		Rate:      clampRate(rate),
	}
	historySamples = append(historySamples, sample)
	compactHistoryLocked(now)
	historyMu.Unlock()

	atomic.AddUint64(&historyVersion, 1)
}

func recordHistoryEvent(eventType string, level int, charging bool, message string) {
	if eventType == "" {
		return
	}
	now := time.Now()
	evt := HistoryEvent{
		Timestamp: now,
		Type:      eventType,
		Level:     level,
		Charging:  charging,
		Message:   message,
	}

	historyMu.Lock()
	historyEvents = append(historyEvents, evt)
	compactHistoryLocked(now)
	historyMu.Unlock()

	atomic.AddUint64(&historyVersion, 1)
}

func markStatusTransition(status string, level int, charging bool) {
	if status == "" {
		return
	}
	historyMu.Lock()
	if status == lastHistoryStatus {
		historyMu.Unlock()
		return
	}
	lastHistoryStatus = status
	historyMu.Unlock()

	switch status {
	case "disconnected":
		recordHistoryEvent("disconnect", level, charging, "Device disconnected")
	case "connected":
		recordHistoryEvent("connect", level, charging, "Device connected")
	}
}

func getHistorySnapshot() ([]HistorySample, []HistoryEvent) {
	historyMu.RLock()
	defer historyMu.RUnlock()

	samples := make([]HistorySample, len(historySamples))
	copy(samples, historySamples)

	events := make([]HistoryEvent, len(historyEvents))
	copy(events, historyEvents)

	return samples, events
}

func getHistoryVersion() uint64 {
	return atomic.LoadUint64(&historyVersion)
}

func buildHistoryResponse(rangeKey string) HistoryResponse {
	duration := parseHistoryRange(rangeKey)
	if duration <= 0 {
		duration = 72 * time.Hour
		rangeKey = "72h"
	}

	to := time.Now()
	from := to.Add(-duration)

	historyMu.RLock()
	samples := make([]HistorySample, len(historySamples))
	copy(samples, historySamples)
	events := make([]HistoryEvent, len(historyEvents))
	copy(events, historyEvents)
	historyMu.RUnlock()

	points := downsampleHistory(samples, from, to, historyDefaultMaxPoints)
	eventsResp := filterHistoryEvents(events, from, to)

	latestLevel := 0
	latestCharging := false
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		latestLevel = last.Level
		latestCharging = last.Charging
	}

	return HistoryResponse{
		Range:          rangeKey,
		From:           from.UnixMilli(),
		To:             to.UnixMilli(),
		Points:         points,
		Events:         eventsResp,
		Version:        getHistoryVersion(),
		LatestLevel:    latestLevel,
		LatestCharging: latestCharging,
	}
}

func parseHistoryRange(rangeKey string) time.Duration {
	switch rangeKey {
	case "24h":
		return 24 * time.Hour
	case "72h", "3d":
		return 72 * time.Hour
	case "7d", "168h":
		return 7 * 24 * time.Hour
	default:
		return 72 * time.Hour
	}
}

func downsampleHistory(samples []HistorySample, from, to time.Time, maxPoints int) []HistoryPointResponse {
	if len(samples) == 0 || !to.After(from) {
		return nil
	}

	var filtered []HistorySample
	var lastBefore *HistorySample
	var nextAfter *HistorySample

	for _, sample := range samples {
		if sample.Timestamp.Before(from) {
			temp := sample
			lastBefore = &temp
			continue
		}
		if sample.Timestamp.After(to) {
			if nextAfter == nil {
				temp := sample
				nextAfter = &temp
			}
			break
		}
		filtered = append(filtered, sample)
	}

	if len(filtered) == 0 {
		if lastBefore != nil {
			filtered = append(filtered, *lastBefore)
		}
		if nextAfter != nil {
			filtered = append(filtered, *nextAfter)
		}
	} else {
		if lastBefore != nil {
			filtered = append([]HistorySample{*lastBefore}, filtered...)
		}
		if nextAfter != nil {
			filtered = append(filtered, *nextAfter)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	if len(filtered) <= maxPoints {
		points := make([]HistoryPointResponse, 0, len(filtered))
		for _, sample := range filtered {
			points = append(points, toHistoryPoint(sample))
		}
		return points
	}

	bucketDuration := to.Sub(from) / time.Duration(maxPoints)
	if bucketDuration <= 0 {
		bucketDuration = time.Minute
	}
	if bucketDuration < time.Minute {
		bucketDuration = time.Minute
	}

	type bucket struct {
		count         int
		levelSum      float64
		rateSum       float64
		chargingTrue  int
		chargingFalse int
		ts            time.Time
	}

	buckets := make([]bucket, maxPoints)
	for _, sample := range filtered {
		idx := int(sample.Timestamp.Sub(from) / bucketDuration)
		if idx < 0 {
			idx = 0
		}
		if idx >= maxPoints {
			idx = maxPoints - 1
		}
		b := &buckets[idx]
		b.count++
		b.levelSum += float64(sample.Level)
		b.rateSum += sample.Rate
		if sample.Charging {
			b.chargingTrue++
		} else {
			b.chargingFalse++
		}
		if sample.Timestamp.After(b.ts) {
			b.ts = sample.Timestamp
		}
	}

	points := make([]HistoryPointResponse, 0, maxPoints)
	for idx, b := range buckets {
		if b.count == 0 {
			continue
		}
		ts := b.ts
		if ts.IsZero() {
			ts = from.Add(time.Duration(idx) * bucketDuration)
		}
		level := b.levelSum / float64(b.count)
		rate := b.rateSum / float64(b.count)
		charging := b.chargingTrue >= b.chargingFalse
		points = append(points, HistoryPointResponse{
			Ts:       ts.UnixMilli(),
			Level:    level,
			Charging: charging,
			Rate:     clampRate(rate),
		})
	}

	if len(points) == 0 && len(filtered) > 0 {
		points = append(points, toHistoryPoint(filtered[len(filtered)-1]))
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Ts < points[j].Ts
	})

	if len(points) > maxPoints {
		points = decimatePoints(points, maxPoints)
	}

	return points
}

func filterHistoryEvents(events []HistoryEvent, from, to time.Time) []HistoryEventResponse {
	if len(events) == 0 {
		return nil
	}
	result := make([]HistoryEventResponse, 0, len(events))
	for _, evt := range events {
		if evt.Timestamp.Before(from) || evt.Timestamp.After(to) {
			continue
		}
		result = append(result, HistoryEventResponse{
			Ts:       evt.Timestamp.UnixMilli(),
			Type:     evt.Type,
			Level:    evt.Level,
			Charging: evt.Charging,
			Message:  evt.Message,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func updateHistoryFromPayload(payload map[string]interface{}) {
	if payload == nil {
		return
	}
	level, haveLevel := extractLevel(payload["level"])
	charging := extractBool(payload["charging"])
	status, _ := payload["status"].(string)
	lastKnown := extractBool(payload["lastKnown"])

	if haveLevel && level >= 0 && level <= 100 {
		if status == "" || status == "connected" || (status == "disconnected" && lastKnown) {
			recordHistorySample(level, charging)
		}
	}

	if status != "" {
		levelForEvent := level
		if !haveLevel {
			levelForEvent = -1
		}
		markStatusTransition(status, levelForEvent, charging)
	}
}

func extractLevel(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return int(math.Round(v)), true
	case float32:
		fv := float64(v)
		if math.IsNaN(fv) || math.IsInf(fv, 0) {
			return 0, false
		}
		return int(math.Round(fv)), true
	default:
		return 0, false
	}
}

func extractBool(value interface{}) bool {
	if b, ok := value.(bool); ok {
		return b
	}
	return false
}

func compactHistoryLocked(now time.Time) {
	cutoff := now.Add(-historyRetention)

	if len(historySamples) > 0 {
		kept := historySamples[:0]
		for _, sample := range historySamples {
			if sample.Timestamp.Before(cutoff) {
				continue
			}
			kept = append(kept, sample)
		}
		historySamples = kept
		if len(historySamples) > maxHistorySamples {
			historySamples = decimateSamples(historySamples, maxHistorySamples)
		}
	}

	if len(historyEvents) > 0 {
		keptEvents := historyEvents[:0]
		for _, evt := range historyEvents {
			if evt.Timestamp.Before(cutoff) {
				continue
			}
			keptEvents = append(keptEvents, evt)
		}
		historyEvents = keptEvents
		if len(historyEvents) > maxHistoryEvents {
			historyEvents = historyEvents[len(historyEvents)-maxHistoryEvents:]
		}
	}
}

func decimateSamples(samples []HistorySample, target int) []HistorySample {
	if target <= 0 || len(samples) <= target {
		copied := make([]HistorySample, len(samples))
		copy(copied, samples)
		return copied
	}
	stride := float64(len(samples)-1) / float64(target-1)
	out := make([]HistorySample, 0, target)
	for i := 0; i < target; i++ {
		idx := int(math.Round(float64(i) * stride))
		if idx >= len(samples) {
			idx = len(samples) - 1
		}
		out = append(out, samples[idx])
	}
	return out
}

func decimatePoints(points []HistoryPointResponse, target int) []HistoryPointResponse {
	if target <= 0 || len(points) <= target {
		return points
	}
	stride := float64(len(points)-1) / float64(target-1)
	out := make([]HistoryPointResponse, 0, target)
	for i := 0; i < target; i++ {
		idx := int(math.Round(float64(i) * stride))
		if idx >= len(points) {
			idx = len(points) - 1
		}
		out = append(out, points[idx])
	}
	return out
}

func toHistoryPoint(sample HistorySample) HistoryPointResponse {
	return HistoryPointResponse{
		Ts:       sample.Timestamp.UnixMilli(),
		Level:    float64(sample.Level),
		Charging: sample.Charging,
		Rate:     clampRate(sample.Rate),
	}
}

func clampRate(rate float64) float64 {
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0
	}
	if rate > maxRateMagnitude {
		return maxRateMagnitude
	}
	if rate < -maxRateMagnitude {
		return -maxRateMagnitude
	}
	return rate
}
