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
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
)

// newWeightFixture builds a deployment fixture with the requested analysis
// configuration and an event recorder that keeps the emitted events.
func newWeightFixture(t *testing.T, analysis *flaggerv1.CanaryAnalysis) (fixture, *record.FakeRecorder) {
	t.Helper()

	cd := newDeploymentTestCanary()
	cd.Spec.Analysis = analysis
	mocks := newDeploymentFixture(cd)

	recorder := record.NewFakeRecorder(1024)
	mocks.ctrl.eventRecorder = recorder
	return mocks, recorder
}

// drainEvents returns every event recorded so far without blocking.
func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

// startWeightRollout initialises the fixture, triggers a new revision and
// leaves the canary in the Progressing phase ready for the analysis cycles.
func startWeightRollout(t *testing.T, mocks fixture) {
	t.Helper()

	// initializing
	mocks.ctrl.advanceCanary("podinfo", "default")

	// make primary ready
	mocks.makePrimaryReady(t)

	// initialized
	mocks.ctrl.advanceCanary("podinfo", "default")
	require.NoError(t, assertPhase(mocks.flaggerClient, "podinfo", flaggerv1.CanaryPhaseInitialized))

	// update
	dep2 := newDeploymentTestDeploymentV2()
	_, err := mocks.kubeClient.AppsV1().Deployments("default").Update(context.TODO(), dep2, metav1.UpdateOptions{})
	require.NoError(t, err)

	// detect changes
	mocks.ctrl.advanceCanary("podinfo", "default")
	require.NoError(t, assertPhase(mocks.flaggerClient, "podinfo", flaggerv1.CanaryPhaseProgressing))
	mocks.makeCanaryReady(t)
}

type routeStep struct {
	primary  int
	canary   int
	mirrored bool
}

// TestScheduler_WeightRoutesPerCycle drives the analysis cycle by cycle and
// checks the routing weights the router receives each time.
func TestScheduler_WeightRoutesPerCycle(t *testing.T) {
	tests := []struct {
		name     string
		analysis *flaggerv1.CanaryAnalysis
		want     []routeStep
	}{
		{
			name: "stepWeight is truncated at maxWeight",
			analysis: &flaggerv1.CanaryAnalysis{
				Threshold:  10,
				StepWeight: 20,
				MaxWeight:  50,
			},
			want: []routeStep{
				{primary: 80, canary: 20},
				{primary: 60, canary: 40},
				{primary: 50, canary: 50},
			},
		},
		{
			name: "stepWeights walks every entry",
			analysis: &flaggerv1.CanaryAnalysis{
				Threshold:   10,
				StepWeights: []int{10, 30, 50},
			},
			want: []routeStep{
				{primary: 90, canary: 10},
				{primary: 70, canary: 30},
				{primary: 50, canary: 50},
			},
		},
		{
			name: "mirror step precedes the first stepWeights entry",
			analysis: &flaggerv1.CanaryAnalysis{
				Threshold:   10,
				StepWeights: []int{10, 30, 50},
				Mirror:      true,
			},
			want: []routeStep{
				{primary: 100, canary: 0, mirrored: true},
				{primary: 90, canary: 10},
				{primary: 70, canary: 30},
				{primary: 50, canary: 50},
			},
		},
		{
			name: "mirror step precedes the first stepWeight",
			analysis: &flaggerv1.CanaryAnalysis{
				Threshold:  10,
				StepWeight: 20,
				MaxWeight:  50,
				Mirror:     true,
			},
			want: []routeStep{
				{primary: 100, canary: 0, mirrored: true},
				{primary: 80, canary: 20},
				{primary: 60, canary: 40},
				{primary: 50, canary: 50},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mocks, _ := newWeightFixture(t, tt.analysis)
			startWeightRollout(t, mocks)

			for i, want := range tt.want {
				mocks.ctrl.advanceCanary("podinfo", "default")

				primary, canary, mirrored, err := mocks.router.GetRoutes(mocks.canary)
				require.NoError(t, err)
				assert.Equal(t, want.primary, primary, "cycle %d primary weight", i)
				assert.Equal(t, want.canary, canary, "cycle %d canary weight", i)
				assert.Equal(t, want.mirrored, mirrored, "cycle %d mirrored", i)
			}

			// the next cycle reaches the ceiling and starts the promotion
			mocks.ctrl.advanceCanary("podinfo", "default")
			require.NoError(t, assertPhase(mocks.flaggerClient, "podinfo", flaggerv1.CanaryPhasePromoting))
		})
	}
}

// TestScheduler_WeightPromotionRoutesPerCycle checks the promotion traffic
// shift, including the stepWeightPromotion of 0 and non-dividing steps.
func TestScheduler_WeightPromotionRoutesPerCycle(t *testing.T) {
	tests := []struct {
		name          string
		analysis      *flaggerv1.CanaryAnalysis
		wantPromotion []routeStep
	}{
		{
			name: "zero step shifts all traffic at once",
			analysis: &flaggerv1.CanaryAnalysis{
				Threshold:  10,
				StepWeight: 50,
				MaxWeight:  50,
			},
			wantPromotion: []routeStep{
				{primary: 100, canary: 0},
			},
		},
		{
			name: "promotion step moves primary forward and truncates at 100",
			analysis: &flaggerv1.CanaryAnalysis{
				Threshold:           10,
				StepWeight:          50,
				MaxWeight:           50,
				StepWeightPromotion: 30,
			},
			wantPromotion: []routeStep{
				{primary: 80, canary: 20},
				{primary: 100, canary: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mocks, _ := newWeightFixture(t, tt.analysis)
			startWeightRollout(t, mocks)

			// reach the ceiling
			mocks.ctrl.advanceCanary("podinfo", "default")
			// enter the promotion
			mocks.ctrl.advanceCanary("podinfo", "default")
			require.NoError(t, assertPhase(mocks.flaggerClient, "podinfo", flaggerv1.CanaryPhasePromoting))

			for i, want := range tt.wantPromotion {
				mocks.ctrl.advanceCanary("podinfo", "default")

				primary, canary, mirrored, err := mocks.router.GetRoutes(mocks.canary)
				require.NoError(t, err)
				assert.Equal(t, want.primary, primary, "promotion cycle %d primary weight", i)
				assert.Equal(t, want.canary, canary, "promotion cycle %d canary weight", i)
				assert.False(t, mirrored)
			}

			require.NoError(t, assertPhase(mocks.flaggerClient, "podinfo", flaggerv1.CanaryPhaseFinalising))
		})
	}
}

// TestScheduler_WeightRejectsInvalidConfig verifies that a configuration that
// cannot be executed stops the rollout without moving traffic.
func TestScheduler_WeightRejectsInvalidConfig(t *testing.T) {
	mocks, events := newWeightFixture(t, &flaggerv1.CanaryAnalysis{
		Threshold:   10,
		StepWeights: []int{-10, 20},
	})
	startWeightRollout(t, mocks)
	drainEvents(events)

	mocks.ctrl.advanceCanary("podinfo", "default")

	primary, canary, mirrored, err := mocks.router.GetRoutes(mocks.canary)
	require.NoError(t, err)
	assert.Equal(t, 100, primary)
	assert.Equal(t, 0, canary)
	assert.False(t, mirrored)

	require.NoError(t, assertPhase(mocks.flaggerClient, "podinfo", flaggerv1.CanaryPhaseFailed))

	got := filterEvents(drainEvents(events), "Invalid analysis configuration")
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "stepWeights[0] -10 must be greater than 0")
}

// TestScheduler_WeightConfigWarnings verifies the executable-but-suspicious
// configurations produce exactly one warning event each, and that the existing
// Advance event text is unchanged.
func TestScheduler_WeightConfigWarnings(t *testing.T) {
	mocks, events := newWeightFixture(t, &flaggerv1.CanaryAnalysis{
		Threshold:  10,
		StepWeight: 20,
		MaxWeight:  50,
	})
	startWeightRollout(t, mocks)
	drainEvents(events)

	// first analysis cycle: warning plus first advance
	mocks.ctrl.advanceCanary("podinfo", "default")
	first := drainEvents(events)

	var warningCount int
	var advanceEvents []string
	for _, e := range first {
		if containsAll(e, "Analysis configuration warning") {
			warningCount++
			assert.Contains(t, e, "stepWeight 20 does not evenly divide maxWeight 50")
		}
		if containsAll(e, "Advance podinfo.default canary weight") {
			advanceEvents = append(advanceEvents, e)
		}
	}
	assert.Equal(t, 1, warningCount)
	require.Len(t, advanceEvents, 1)
	assert.Contains(t, advanceEvents[0], "Advance podinfo.default canary weight 20")

	// later cycles must not repeat the warning and must keep the Advance text
	for _, wantWeight := range []string{"40", "50"} {
		mocks.ctrl.advanceCanary("podinfo", "default")
		cycleEvents := drainEvents(events)

		assert.Empty(t, filterEvents(cycleEvents, "Analysis configuration warning"),
			"the configuration warning must be reported once per rollout")

		advances := filterEvents(cycleEvents, "Advance podinfo.default canary weight")
		require.Len(t, advances, 1)
		assert.Contains(t, advances[0], fmt.Sprintf("Advance podinfo.default canary weight %s", wantWeight))
	}
}

// TestScheduler_WeightConflictEvents verifies that stepWeights wins over
// maxWeight, the conflict is reported, and the no-hook promotion path keeps
// the existing "primary weight" event text.
func TestScheduler_WeightConflictEvents(t *testing.T) {
	mocks, events := newWeightFixture(t, &flaggerv1.CanaryAnalysis{
		Threshold:   10,
		StepWeights: []int{10, 30},
		MaxWeight:   20,
	})
	startWeightRollout(t, mocks)
	drainEvents(events)

	mocks.ctrl.advanceCanary("podinfo", "default")

	first := drainEvents(events)
	warnings := filterEvents(first, "Analysis configuration warning")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "maxWeight 20 is ignored")
	assert.Contains(t, warnings[0], "last stepWeights value 30")

	// maxWeight is ignored: the second step reaches 30, not 20
	mocks.ctrl.advanceCanary("podinfo", "default")
	drainEvents(events)

	primary, canary, _, err := mocks.router.GetRoutes(mocks.canary)
	require.NoError(t, err)
	assert.Equal(t, 70, primary)
	assert.Equal(t, 30, canary)

	// the next cycle promotes
	mocks.ctrl.advanceCanary("podinfo", "default")
	promoteEvents := drainEvents(events)
	assert.Contains(t, promoteEvents, eventWithMessage(
		"Copying podinfo.default template spec to podinfo-primary.default"))
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func filterEvents(events []string, substr string) []string {
	var out []string
	for _, e := range events {
		if strings.Contains(e, substr) {
			out = append(out, e)
		}
	}
	return out
}

func eventWithMessage(message string) string {
	return "Normal Synced " + message
}
