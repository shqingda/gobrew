package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

type comparison struct {
	Scenario     string  `json:"scenario"`
	GobrewMedian float64 `json:"gobrew_median_ms"`
	BrewMedian   float64 `json:"brew_median_ms"`
	Passed       bool    `json:"passed"`
}

type performanceGate struct {
	Passed      bool         `json:"passed"`
	Comparisons []comparison `json:"comparisons,omitempty"`
}

func statistics(samples []float64) (median, p95 float64) {
	if len(samples) == 0 {
		return 0, 0
	}
	ordered := slices.Clone(samples)
	slices.Sort(ordered)
	median = ordered[len(ordered)/2]
	if len(ordered)%2 == 0 {
		median = (ordered[len(ordered)/2-1] + median) / 2
	}
	return median, ordered[(95*len(ordered)+99)/100-1]
}

// A missing or incomplete baseline can never satisfy the release gate. No-op
// samples are required evidence, but only cold and warm medians are thresholds.
func checkPerformance(measurements []measurement) (performanceGate, error) {
	gate := performanceGate{}
	byName := make(map[string]measurement, len(measurements))
	for _, m := range measurements {
		if _, duplicate := byName[m.Name]; duplicate {
			return gate, fmt.Errorf("performance gate: duplicate measurement %s", m.Name)
		}
		byName[m.Name] = m
	}
	count := len(byName["gobrew.cold"].Milliseconds)
	for _, name := range []string{"gobrew.cold", "gobrew.warm", "gobrew.noop", "brew.cold", "brew.warm", "brew.noop"} {
		m := byName[name]
		if count < 5 || len(m.Milliseconds) != count {
			return gate, fmt.Errorf("performance gate: %s needs at least five complete matching samples", name)
		}
		for _, duration := range m.Milliseconds {
			if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				return gate, fmt.Errorf("performance gate: %s has an invalid duration", name)
			}
		}
	}
	var failures []error
	for _, scenario := range []string{"cold", "warm"} {
		gb, _ := statistics(byName["gobrew."+scenario].Milliseconds)
		brew, _ := statistics(byName["brew."+scenario].Milliseconds)
		passed := gb < brew
		gate.Comparisons = append(gate.Comparisons, comparison{Scenario: scenario, GobrewMedian: gb, BrewMedian: brew, Passed: passed})
		if !passed {
			failures = append(failures, fmt.Errorf("%s median %.3f ms is not faster than Homebrew %.3f ms", scenario, gb, brew))
		}
	}
	gate.Passed = len(failures) == 0
	return gate, errors.Join(failures...)
}
