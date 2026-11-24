/*
Copyright 2025 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
)

func TestEffectiveMaxWeight(t *testing.T) {
	tests := []struct {
		name string
		cfg  WeightConfig
		want int
	}{
		{
			name: "stepWeights last entry wins",
			cfg:  WeightConfig{StepWeights: []int{10, 30, 50}},
			want: 50,
		},
		{
			name: "stepWeights last entry is capped at total weight",
			cfg:  WeightConfig{StepWeights: []int{10, 30, 150}},
			want: 100,
		},
		{
			name: "maxWeight is ignored when stepWeights is set",
			cfg:  WeightConfig{StepWeights: []int{10, 30}, MaxWeight: 20},
			want: 30,
		},
		{
			name: "maxWeight is used without stepWeights",
			cfg:  WeightConfig{StepWeight: 20, MaxWeight: 50},
			want: 50,
		},
		{
			name: "maxWeight defaults to total weight",
			cfg:  WeightConfig{StepWeight: 20},
			want: 100,
		},
		{
			name: "maxWeight is capped at total weight",
			cfg:  WeightConfig{StepWeight: 20, MaxWeight: 150},
			want: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EffectiveMaxWeight(tt.cfg))
		})
	}
}

func TestNextAnalysisWeight(t *testing.T) {
	tests := []struct {
		name  string
		cfg   WeightConfig
		state WeightState
		want  WeightDecision
	}{
		{
			name:  "stepWeight first step",
			cfg:   WeightConfig{StepWeight: 20, MaxWeight: 50},
			state: WeightState{PrimaryWeight: 100},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 80, CanaryWeight: 20, StepWeight: 20},
		},
		{
			name:  "stepWeight truncated at maxWeight",
			cfg:   WeightConfig{StepWeight: 20, MaxWeight: 50},
			state: WeightState{PrimaryWeight: 60, CanaryWeight: 40},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 50, CanaryWeight: 50, StepWeight: 10},
		},
		{
			name:  "stepWeight promotes at the ceiling",
			cfg:   WeightConfig{StepWeight: 20, MaxWeight: 50},
			state: WeightState{PrimaryWeight: 50, CanaryWeight: 50},
			want:  WeightDecision{Kind: WeightStepPromote, PrimaryWeight: 50, CanaryWeight: 50},
		},
		{
			name:  "stepWeight without maxWeight promotes at total weight",
			cfg:   WeightConfig{StepWeight: 30},
			state: WeightState{PrimaryWeight: 10, CanaryWeight: 90},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 0, CanaryWeight: 100, StepWeight: 10},
		},
		{
			name:  "stepWeights initial step",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}},
			state: WeightState{PrimaryWeight: 100},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 90, CanaryWeight: 10, StepWeight: 10},
		},
		{
			name:  "stepWeights next configured step",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}},
			state: WeightState{PrimaryWeight: 90, CanaryWeight: 10},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 70, CanaryWeight: 30, StepWeight: 20},
		},
		{
			name:  "stepWeights drift 33 rounds up to 50",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}},
			state: WeightState{PrimaryWeight: 67, CanaryWeight: 33},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 50, CanaryWeight: 50, StepWeight: 17},
		},
		{
			name:  "stepWeights drift 52 past the last entry promotes",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}},
			state: WeightState{PrimaryWeight: 48, CanaryWeight: 52},
			want:  WeightDecision{Kind: WeightStepPromote, PrimaryWeight: 48, CanaryWeight: 52},
		},
		{
			name:  "stepWeights last entry reached promotes",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}},
			state: WeightState{PrimaryWeight: 50, CanaryWeight: 50},
			want:  WeightDecision{Kind: WeightStepPromote, PrimaryWeight: 50, CanaryWeight: 50},
		},
		{
			name:  "stepWeights drift between entries takes the next entry only",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50, 80}},
			state: WeightState{PrimaryWeight: 55, CanaryWeight: 45},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 50, CanaryWeight: 50, StepWeight: 5},
		},
		{
			name:  "stepWeights drift past the last entry promotes",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50, 80}},
			state: WeightState{PrimaryWeight: 10, CanaryWeight: 90},
			want:  WeightDecision{Kind: WeightStepPromote, PrimaryWeight: 10, CanaryWeight: 90},
		},
		{
			name:  "stepWeights maxWeight is ignored",
			cfg:   WeightConfig{StepWeights: []int{10, 30}, MaxWeight: 20},
			state: WeightState{PrimaryWeight: 90, CanaryWeight: 10},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 70, CanaryWeight: 30, StepWeight: 20},
		},
		{
			name:  "mirror first step keeps traffic on primary",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}, Mirror: true},
			state: WeightState{PrimaryWeight: 100},
			want:  WeightDecision{Kind: WeightStepMirror, PrimaryWeight: 100, CanaryWeight: 0, Mirrored: true},
		},
		{
			name:  "mirror second step shifts the first stepWeight and does not consume it",
			cfg:   WeightConfig{StepWeights: []int{10, 30, 50}, Mirror: true},
			state: WeightState{PrimaryWeight: 100, Mirrored: true},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 90, CanaryWeight: 10, StepWeight: 10},
		},
		{
			name:  "mirror second step with stepWeight",
			cfg:   WeightConfig{StepWeight: 20, MaxWeight: 50, Mirror: true},
			state: WeightState{PrimaryWeight: 100, Mirrored: true},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 80, CanaryWeight: 20, StepWeight: 20},
		},
		{
			name:  "stepWeight takes priority when stepWeights is also set",
			cfg:   WeightConfig{StepWeight: 20, StepWeights: []int{10, 30, 50}},
			state: WeightState{PrimaryWeight: 100},
			want:  WeightDecision{Kind: WeightStepAdvance, PrimaryWeight: 80, CanaryWeight: 20, StepWeight: 20},
		},
		{
			name:  "no step configured promotes",
			cfg:   WeightConfig{},
			state: WeightState{PrimaryWeight: 100},
			want:  WeightDecision{Kind: WeightStepPromote, PrimaryWeight: 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NextAnalysisWeight(tt.cfg, tt.state))
		})
	}
}

// TestWeightSequence drives NextAnalysisWeight cycle by cycle and asserts the
// whole canary weight sequence, not only the final step.
func TestWeightSequence(t *testing.T) {
	tests := []struct {
		name string
		cfg  WeightConfig
		want []int
	}{
		{
			name: "stepWeight 20 with maxWeight 50 is truncated",
			cfg:  WeightConfig{StepWeight: 20, MaxWeight: 50},
			want: []int{20, 40, 50},
		},
		{
			name: "stepWeights walks every entry",
			cfg:  WeightConfig{StepWeights: []int{10, 30, 50}},
			want: []int{10, 30, 50},
		},
		{
			name: "stepWeights with mirror keeps the mirror step separate",
			cfg:  WeightConfig{StepWeights: []int{10, 30, 50}, Mirror: true},
			want: []int{0, 10, 30, 50},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			state := WeightState{PrimaryWeight: totalTrafficWeight}
			for i := 0; i < 20; i++ {
				d := NextAnalysisWeight(tt.cfg, state)
				if d.Kind == WeightStepPromote {
					break
				}
				got = append(got, d.CanaryWeight)
				state = WeightState{PrimaryWeight: d.PrimaryWeight, CanaryWeight: d.CanaryWeight, Mirrored: d.Mirrored}
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextPromotionWeight(t *testing.T) {
	tests := []struct {
		name          string
		cfg           WeightConfig
		primaryWeight int
		canaryWeight  int
		want          PromotionDecision
	}{
		{
			name:          "zero step routes everything to primary",
			cfg:           WeightConfig{},
			primaryWeight: 50,
			canaryWeight:  50,
			want:          PromotionDecision{PrimaryWeight: 100, CanaryWeight: 0, Finalise: true},
		},
		{
			name:          "step moves primary forward",
			cfg:           WeightConfig{StepWeightPromotion: 20},
			primaryWeight: 50,
			canaryWeight:  50,
			want:          PromotionDecision{PrimaryWeight: 70, CanaryWeight: 30, StepWeight: 20},
		},
		{
			name:          "last step is truncated at total weight",
			cfg:           WeightConfig{StepWeightPromotion: 20},
			primaryWeight: 90,
			canaryWeight:  10,
			want:          PromotionDecision{PrimaryWeight: 100, CanaryWeight: 0, StepWeight: 20, Finalise: true},
		},
		{
			name:          "canary weight never goes negative",
			cfg:           WeightConfig{StepWeightPromotion: 80},
			primaryWeight: 40,
			canaryWeight:  10,
			want:          PromotionDecision{PrimaryWeight: 100, CanaryWeight: 0, StepWeight: 80, Finalise: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NextPromotionWeight(tt.cfg, tt.primaryWeight, tt.canaryWeight))
		})
	}
}

func TestValidateWeightConfig(t *testing.T) {
	tests := []struct {
		name         string
		cfg          WeightConfig
		wantErrors   int
		errorSubstr  []string
		wantWarnings int
		warnSubstr   []string
	}{
		{
			name: "valid stepWeight",
			cfg:  WeightConfig{StepWeight: 20, MaxWeight: 60},
		},
		{
			name: "valid stepWeights",
			cfg:  WeightConfig{StepWeights: []int{10, 30, 50}},
		},
		{
			name:        "stepWeights containing zero",
			cfg:         WeightConfig{StepWeights: []int{0, 10}},
			wantErrors:  1,
			errorSubstr: []string{"must be greater than 0"},
		},
		{
			name:        "stepWeights containing a negative value",
			cfg:         WeightConfig{StepWeights: []int{-10, 10}},
			wantErrors:  1,
			errorSubstr: []string{"must be greater than 0"},
		},
		{
			name:        "stepWeights above total weight",
			cfg:         WeightConfig{StepWeights: []int{10, 150}},
			wantErrors:  1,
			errorSubstr: []string{"must not exceed 100"},
		},
		{
			name:        "stepWeights with duplicates breaks strict increase",
			cfg:         WeightConfig{StepWeights: []int{10, 10, 30}},
			wantErrors:  1,
			errorSubstr: []string{"strictly increasing"},
		},
		{
			name:        "stepWeights decreasing",
			cfg:         WeightConfig{StepWeights: []int{30, 10, 50}},
			wantErrors:  1,
			errorSubstr: []string{"strictly increasing"},
		},
		{
			name:        "negative stepWeight",
			cfg:         WeightConfig{StepWeight: -10},
			wantErrors:  1,
			errorSubstr: []string{"must be greater than 0"},
		},
		{
			name:        "stepWeight above total weight",
			cfg:         WeightConfig{StepWeight: 150},
			wantErrors:  1,
			errorSubstr: []string{"must not exceed 100"},
		},
		{
			name:        "negative stepWeightPromotion",
			cfg:         WeightConfig{StepWeight: 20, StepWeightPromotion: -10},
			wantErrors:  1,
			errorSubstr: []string{"stepWeightPromotion"},
		},
		{
			name:        "stepWeightPromotion above total weight",
			cfg:         WeightConfig{StepWeight: 20, StepWeightPromotion: 150},
			wantErrors:  1,
			errorSubstr: []string{"stepWeightPromotion"},
		},
		{
			name:         "stepWeight does not divide maxWeight",
			cfg:          WeightConfig{StepWeight: 20, MaxWeight: 50},
			wantWarnings: 1,
			warnSubstr:   []string{"does not evenly divide"},
		},
		{
			name:         "stepWeight greater than maxWeight",
			cfg:          WeightConfig{StepWeight: 60, MaxWeight: 50},
			wantWarnings: 1,
			warnSubstr:   []string{"greater than maxWeight"},
		},
		{
			name:         "stepWeights and conflicting maxWeight",
			cfg:          WeightConfig{StepWeights: []int{10, 30}, MaxWeight: 20},
			wantWarnings: 1,
			warnSubstr:   []string{"effective maximum weight is the last stepWeights value 30"},
		},
		{
			name: "stepWeights and matching maxWeight produces no warning",
			cfg:  WeightConfig{StepWeights: []int{10, 30}, MaxWeight: 30},
		},
		{
			name:        "no step configured",
			cfg:         WeightConfig{MaxWeight: 50},
			wantErrors:  1,
			errorSubstr: []string{"either stepWeight or stepWeights"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, warnings := ValidateWeightConfig(tt.cfg)
			require.Len(t, errs, tt.wantErrors)
			require.Len(t, warnings, tt.wantWarnings)
			for i, sub := range tt.errorSubstr {
				assert.Contains(t, errs[i].Error(), sub)
			}
			for i, sub := range tt.warnSubstr {
				assert.Contains(t, warnings[i], sub)
			}
		})
	}
}

// TestWeightProgressionProperty generates valid configurations from a fixed
// seed, simulates the analysis to promotion and checks the invariants that must
// hold for every input.
func TestWeightProgressionProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20251125))

	for i := 0; i < 500; i++ {
		cfg := randomWeightConfig(rng)
		errs, _ := ValidateWeightConfig(cfg)
		require.Empty(t, errs, "generated an invalid config: %#v", cfg)

		maxWeight := EffectiveMaxWeight(cfg)
		state := WeightState{PrimaryWeight: totalTrafficWeight}
		prevCanary := 0
		advances := 0
		mirrorSteps := 0
		promoted := false

		for cycle := 0; cycle < 500; cycle++ {
			d := NextAnalysisWeight(cfg, state)
			if d.Kind == WeightStepPromote {
				promoted = true
				break
			}

			assert.Equal(t, totalTrafficWeight, d.PrimaryWeight+d.CanaryWeight,
				"weights must always sum to %d: %#v", totalTrafficWeight, cfg)
			assert.GreaterOrEqual(t, d.CanaryWeight, prevCanary,
				"canary weight must not decrease: %#v", cfg)
			assert.LessOrEqual(t, d.CanaryWeight, maxWeight,
				"canary weight must not exceed maxWeight: %#v", cfg)

			if d.Kind == WeightStepMirror {
				mirrorSteps++
				assert.Equal(t, 0, d.CanaryWeight)
			} else {
				advances++
				assert.Greater(t, d.StepWeight, 0, "an advance must move traffic: %#v", cfg)
			}

			prevCanary = d.CanaryWeight
			state = WeightState{PrimaryWeight: d.PrimaryWeight, CanaryWeight: d.CanaryWeight, Mirrored: d.Mirrored}
		}

		assert.True(t, promoted, "progression must reach promotion: %#v", cfg)
		assert.LessOrEqual(t, mirrorSteps, 1, "at most one mirror step: %#v", cfg)

		// the number of advance steps is bounded by the number of configured
		// steps, or by the number of stepWeight increments needed to reach the
		// ceiling
		bound := len(cfg.StepWeights)
		if len(cfg.StepWeights) == 0 {
			bound = (maxWeight + cfg.StepWeight - 1) / cfg.StepWeight
		}
		assert.LessOrEqual(t, advances, bound, "too many advance steps: %#v", cfg)
	}
}

func randomWeightConfig(rng *rand.Rand) WeightConfig {
	cfg := WeightConfig{
		Mirror: rng.Intn(2) == 0,
	}
	if rng.Intn(2) == 0 {
		cfg.StepWeight = rng.Intn(totalTrafficWeight) + 1
		if rng.Intn(2) == 0 {
			cfg.MaxWeight = rng.Intn(totalTrafficWeight) + 1
		}
		return cfg
	}

	n := rng.Intn(5) + 1
	seen := map[int]bool{}
	for len(cfg.StepWeights) < n {
		w := rng.Intn(totalTrafficWeight) + 1
		if seen[w] {
			continue
		}
		seen[w] = true
		cfg.StepWeights = append(cfg.StepWeights, w)
	}
	// sort ascending so the configuration is strictly increasing
	for i := 1; i < len(cfg.StepWeights); i++ {
		for j := i; j > 0 && cfg.StepWeights[j-1] > cfg.StepWeights[j]; j-- {
			cfg.StepWeights[j-1], cfg.StepWeights[j] = cfg.StepWeights[j], cfg.StepWeights[j-1]
		}
	}
	return cfg
}

// TestWeightConfigFromAnalysis ensures the pure config mirrors the analysis
// fields and does not alias the stepWeights slice.
func TestWeightConfigFromAnalysis(t *testing.T) {
	cfg := WeightConfigFromAnalysis(nil)
	assert.Equal(t, WeightConfig{}, cfg)

	analysis := &flaggerv1.CanaryAnalysis{
		StepWeight:          10,
		StepWeights:         []int{10, 30},
		MaxWeight:           50,
		StepWeightPromotion: 20,
		Mirror:              true,
	}
	got := WeightConfigFromAnalysis(analysis)
	assert.Equal(t, 10, got.StepWeight)
	assert.Equal(t, []int{10, 30}, got.StepWeights)
	assert.Equal(t, 50, got.MaxWeight)
	assert.Equal(t, 20, got.StepWeightPromotion)
	assert.True(t, got.Mirror)

	analysis.StepWeights[0] = 99
	assert.Equal(t, []int{10, 30}, got.StepWeights, "the config must not alias the analysis slice")
}

func TestMinInt(t *testing.T) {
	assert.Equal(t, 1, minInt(1, 2))
	assert.Equal(t, 1, minInt(2, 1))
}
