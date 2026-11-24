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
	"fmt"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
)

// totalTrafficWeight is the sum of the primary and canary traffic weights.
// Weights are always expressed as a share of this total.
const totalTrafficWeight = 100

// WeightConfig is the traffic weight configuration of a canary analysis.
// It is a plain value so that the weight progression can be computed without
// a router or a Kubernetes client.
type WeightConfig struct {
	// StepWeight is the fixed analysis step. It takes precedence over StepWeights
	// when both are set (the CRD forbids this combination, but the pure
	// functions still pin the priority so the behaviour is testable).
	StepWeight int
	// StepWeights is the ordered list of analysis weights.
	StepWeights []int
	// MaxWeight is the upper bound of the canary weight during the analysis.
	MaxWeight int
	// StepWeightPromotion is the step used while shifting traffic back to primary.
	StepWeightPromotion int
	// Mirror enables the traffic mirroring step before the analysis starts.
	Mirror bool
}

// WeightState is the routing state observed from the router.
type WeightState struct {
	PrimaryWeight int
	CanaryWeight  int
	Mirrored      bool
}

// WeightStepKind describes what the next analysis cycle should do.
type WeightStepKind int

const (
	// WeightStepAdvance moves traffic to the canary.
	WeightStepAdvance WeightStepKind = iota
	// WeightStepMirror enables traffic mirroring without shifting any weight.
	WeightStepMirror
	// WeightStepPromote means the maximum weight has been reached and the
	// rollout must enter the promotion phase.
	WeightStepPromote
)

// WeightDecision is the outcome of a single analysis cycle.
type WeightDecision struct {
	Kind          WeightStepKind
	PrimaryWeight int
	CanaryWeight  int
	Mirrored      bool
	// StepWeight is the canary weight increment applied by this cycle. It is
	// zero for mirror and promotion decisions.
	StepWeight int
}

// PromotionDecision is the outcome of a single promotion cycle.
type PromotionDecision struct {
	PrimaryWeight int
	CanaryWeight  int
	// StepWeight is the primary weight increment applied by this cycle.
	StepWeight int
	// Finalise is true when all traffic is on the primary and the rollout can
	// move to the finalising phase.
	Finalise bool
}

// WeightConfigFromAnalysis returns a copy of the weight configuration of a
// canary analysis. A nil analysis yields the zero configuration.
func WeightConfigFromAnalysis(analysis *flaggerv1.CanaryAnalysis) WeightConfig {
	if analysis == nil {
		return WeightConfig{}
	}
	cfg := WeightConfig{
		StepWeight:          analysis.StepWeight,
		MaxWeight:           analysis.MaxWeight,
		StepWeightPromotion: analysis.StepWeightPromotion,
		Mirror:              analysis.Mirror,
	}
	if analysis.StepWeights != nil {
		cfg.StepWeights = append([]int(nil), analysis.StepWeights...)
	}
	return cfg
}

// EffectiveMaxWeight returns the highest canary weight the analysis may reach.
// When stepWeights is set its last element wins and maxWeight is ignored,
// otherwise maxWeight is used, falling back to the total traffic weight.
//
// Values above the total traffic weight are clamped so the reported limit is
// always routable; the configuration itself is reported by ValidateWeightConfig.
func EffectiveMaxWeight(cfg WeightConfig) int {
	if n := len(cfg.StepWeights); n > 0 {
		return minInt(totalTrafficWeight, cfg.StepWeights[n-1])
	}
	if cfg.MaxWeight > 0 {
		return minInt(totalTrafficWeight, cfg.MaxWeight)
	}
	return totalTrafficWeight
}

// ValidateWeightConfig classifies weight configuration problems.
//
// The returned errors are configurations that cannot be executed; the returned
// warnings are configurations that can be executed but are most likely not
// what the user intended. The CRD only constrains which of iterations,
// stepWeight and stepWeights is present, so the value ranges and the ordering
// are checked here at runtime.
func ValidateWeightConfig(cfg WeightConfig) ([]error, []string) {
	var errs []error
	var warnings []string

	if cfg.StepWeightPromotion < 0 {
		errs = append(errs, fmt.Errorf("stepWeightPromotion %d must not be negative", cfg.StepWeightPromotion))
	} else if cfg.StepWeightPromotion > totalTrafficWeight {
		errs = append(errs, fmt.Errorf("stepWeightPromotion %d must not exceed %d", cfg.StepWeightPromotion, totalTrafficWeight))
	}
	switch {
	case cfg.StepWeight < 0:
		errs = append(errs, fmt.Errorf("stepWeight %d must be greater than 0", cfg.StepWeight))
	case cfg.StepWeight > totalTrafficWeight:
		errs = append(errs, fmt.Errorf("stepWeight %d must not exceed %d", cfg.StepWeight, totalTrafficWeight))
	case cfg.StepWeight > 0:
		if cfg.MaxWeight > 0 {
			if cfg.StepWeight > cfg.MaxWeight {
				warnings = append(warnings, fmt.Sprintf(
					"stepWeight %d is greater than maxWeight %d, the first step will be truncated to maxWeight",
					cfg.StepWeight, cfg.MaxWeight))
			} else if cfg.MaxWeight%cfg.StepWeight != 0 {
				warnings = append(warnings, fmt.Sprintf(
					"stepWeight %d does not evenly divide maxWeight %d, the last step will be truncated to maxWeight",
					cfg.StepWeight, cfg.MaxWeight))
			}
		}
		if cfg.MaxWeight > totalTrafficWeight {
			warnings = append(warnings, fmt.Sprintf(
				"maxWeight %d is greater than %d, the effective maximum weight is %d",
				cfg.MaxWeight, totalTrafficWeight, totalTrafficWeight))
		}
	case len(cfg.StepWeights) > 0:
		prev := 0
		for i, w := range cfg.StepWeights {
			switch {
			case w <= 0:
				errs = append(errs, fmt.Errorf("stepWeights[%d] %d must be greater than 0", i, w))
			case w > totalTrafficWeight:
				errs = append(errs, fmt.Errorf("stepWeights[%d] %d must not exceed %d", i, w, totalTrafficWeight))
			}
			if i > 0 && w <= prev {
				errs = append(errs, fmt.Errorf(
					"stepWeights must be strictly increasing, stepWeights[%d] %d is not greater than stepWeights[%d] %d",
					i, w, i-1, prev))
			}
			prev = w
		}
		last := cfg.StepWeights[len(cfg.StepWeights)-1]
		if cfg.MaxWeight > 0 && cfg.MaxWeight != last {
			warnings = append(warnings, fmt.Sprintf(
				"maxWeight %d is ignored because stepWeights is set, the effective maximum weight is the last stepWeights value %d",
				cfg.MaxWeight, last))
		}
	default:
		errs = append(errs, fmt.Errorf("either stepWeight or stepWeights must be set to a value greater than 0"))
	}

	return errs, warnings
}

// NextAnalysisWeight computes the next analysis cycle from the observed routing
// state. The canary weight never exceeds EffectiveMaxWeight and never decreases:
// when the observed weight does not match a configured step weight the first
// configured step strictly above it is used, and when no step is above it the
// rollout is promoted.
func NextAnalysisWeight(cfg WeightConfig, state WeightState) WeightDecision {
	maxWeight := EffectiveMaxWeight(cfg)

	// Mirror mode does one step of mirroring before shifting traffic to the
	// canary. The mirror step consumes no stepWeights entry.
	if cfg.Mirror && state.CanaryWeight == 0 && !state.Mirrored {
		return WeightDecision{
			Kind:          WeightStepMirror,
			PrimaryWeight: totalTrafficWeight,
			CanaryWeight:  0,
			Mirrored:      true,
		}
	}

	target, advance := nextAnalysisTarget(cfg, state.CanaryWeight)
	if !advance {
		return WeightDecision{
			Kind:          WeightStepPromote,
			PrimaryWeight: state.PrimaryWeight,
			CanaryWeight:  state.CanaryWeight,
			Mirrored:      state.Mirrored,
		}
	}

	if target > maxWeight {
		target = maxWeight
	}
	if target < state.CanaryWeight {
		target = state.CanaryWeight
	}

	return WeightDecision{
		Kind:          WeightStepAdvance,
		PrimaryWeight: totalTrafficWeight - target,
		CanaryWeight:  target,
		Mirrored:      false,
		StepWeight:    target - state.CanaryWeight,
	}
}

// nextAnalysisTarget returns the canary weight the next analysis cycle should
// reach. When advance is false the analysis is finished and the rollout should
// be promoted.
func nextAnalysisTarget(cfg WeightConfig, current int) (target int, advance bool) {
	if current >= EffectiveMaxWeight(cfg) {
		return current, false
	}

	if cfg.StepWeight != 0 || len(cfg.StepWeights) == 0 {
		if cfg.StepWeight <= 0 {
			return current, false
		}
		target = current + cfg.StepWeight
		if target > EffectiveMaxWeight(cfg) {
			target = EffectiveMaxWeight(cfg)
		}
		if target <= current {
			return current, false
		}
		return target, true
	}

	for _, w := range cfg.StepWeights {
		if w > current {
			if w > EffectiveMaxWeight(cfg) {
				w = EffectiveMaxWeight(cfg)
			}
			if w <= current {
				continue
			}
			return w, true
		}
	}

	return current, false
}

// NextPromotionWeight computes the next promotion cycle. With a zero step all
// traffic goes to the primary at once, otherwise the primary weight grows by
// StepWeightPromotion per cycle, never exceeding the total weight, and the
// canary weight never becomes negative.
func NextPromotionWeight(cfg WeightConfig, primaryWeight, canaryWeight int) PromotionDecision {
	if cfg.StepWeightPromotion <= 0 {
		return PromotionDecision{
			PrimaryWeight: totalTrafficWeight,
			CanaryWeight:  0,
			Finalise:      true,
		}
	}

	primaryWeight += cfg.StepWeightPromotion
	if primaryWeight > totalTrafficWeight {
		primaryWeight = totalTrafficWeight
	}

	canaryWeight -= cfg.StepWeightPromotion
	if canaryWeight < 0 {
		canaryWeight = 0
	}
	if maxCanary := totalTrafficWeight - primaryWeight; canaryWeight > maxCanary {
		canaryWeight = maxCanary
	}

	return PromotionDecision{
		PrimaryWeight: primaryWeight,
		CanaryWeight:  canaryWeight,
		StepWeight:    cfg.StepWeightPromotion,
		Finalise:      primaryWeight >= totalTrafficWeight,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
