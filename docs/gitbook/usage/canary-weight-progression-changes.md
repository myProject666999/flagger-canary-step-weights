# Progressive canary weight progression — delivery notes

The weight progression of a progressive canary rollout used to be spread across
`maxWeight`, `totalWeight`, `nextStepWeight`, `runCanary`,
`runPromotionTrafficShift` and the tail of `advanceCanary`. It is now expressed
by router- and clientset-free pure functions in `pkg/controller/weight.go`:

| function | responsibility |
| --- | --- |
| `WeightConfigFromAnalysis` | copy the weight fields of a `CanaryAnalysis` into a plain `WeightConfig` |
| `EffectiveMaxWeight` | the analysis ceiling: last `stepWeights` entry, else `maxWeight`, else 100 |
| `ValidateWeightConfig` | split weight configuration problems into blocking errors and advisory warnings |
| `NextAnalysisWeight` | next analysis cycle from the observed `WeightState`: mirror, advance or promote |
| `NextPromotionWeight` | next promotion cycle, including the zero-step "all at once" case |

The controller still owns the side effects: `runCanary`,
`runPromotionTrafficShift` and the failure counter path keep calling
`meshRouter.SetRoutes`, `canaryController.SetStatusWeight`,
`canaryController.SetStatusFailedChecks` and `canaryController.SetStatusPhase`
exactly as before. The failure-count rule (`FailedChecks >= threshold`) was not
changed and no new budget semantics were added.

## Behaviour changes

### `maxWeight` is a real ceiling

Before:

* `stepWeight: 20` with `maxWeight: 50` produced `20, 40, 60`.
* With `stepWeights`, a weight that did not match a configured entry made
  `nextStepWeight` return `100 - canaryWeight`, and `runCanary` capped the
  result at `100` rather than `maxWeight`.

Now:

* `stepWeight: 20` with `maxWeight: 50` produces `20, 40, 50`; the last step is
  truncated to the ceiling.
* A mismatched weight never exceeds the ceiling. Example:
  `stepWeights: [10, 30, 50]` with a reported canary weight of `33` advances to
  `50` (a step of 17), not `100`.
* A reported weight of `52` with `stepWeights: [10, 30, 50]` is above the last
  entry and promotes immediately instead of advancing again.

### Mismatch resolution rounds up

When the router-reported weight does not equal a `stepWeights` entry, the next
target is the **first entry strictly greater** than the current weight. Rounding
down was rejected because it would make the weight move backwards; the canary
weight is never allowed to decrease. If no entry is above the current weight,
the rollout is promoted.

`stepWeight` and `stepWeights` remain mutually exclusive at the CRD level
(`analysis.oneOf` requires exactly one of `iterations`, `stepWeight`,
`stepWeights`). The pure functions still give `stepWeight` priority when both
are present so that the code-level rule is pinned by a test; no runtime branch
was added to reject the combination, since the API server already does.

### `stepWeights` wins over `maxWeight`, with a warning

`maxWeight` and `stepWeights` can coexist in the CRD. `EffectiveMaxWeight`
continues to use the last `stepWeights` entry and silently ignoring `maxWeight`
before; now the conflict is reported as a warning event:

```
Analysis configuration warning: maxWeight 20 is ignored because stepWeights is set, the effective maximum weight is the last stepWeights value 30
```

Rejecting the combination outright was left out on purpose: existing resources
may already set both, and turning the warning into an error would stop them.

### Configuration validation

Runtime checks now cover what the CRD does not: `stepWeights` items are declared
only as `type: number` with no range or ordering, and fractional values already
fail at `[]int` deserialization. The checks are split in two:

Blocking (error event `Invalid analysis configuration: ...`, the rollout is
marked `Failed` with all traffic routed back to primary, no traffic is moved):

* `stepWeight` negative or above 100
* any `stepWeights` entry zero, negative or above 100
* `stepWeights` not strictly increasing (duplicates or descending)
* `stepWeightPromotion` negative or above 100
* neither `stepWeight` nor `stepWeights` positive

Advisory (warning event once per rollout, progression continues):

* `stepWeight` does not evenly divide `maxWeight`
* `stepWeight` greater than `maxWeight`
* `maxWeight` greater than 100
* `maxWeight` set with `stepWeights` and differing from the last entry

Validation runs for the progressive canary strategy only, during `Progressing`
and `Waiting`, and is skipped for `skipAnalysis`. Once a rollout reaches
`Promoting` the traffic is already moving back to primary and is not
re-validated, so a mid-promotion spec edit cannot jump the traffic. If the CRD
later gains range or ordering constraints, the overlapping runtime checks can be
dropped without changing the remaining ones.

### Mirror start is a separate, weight-free step

When `analysis.mirror` is true and the canary weight is 0, the first cycle is a
pure mirror step: primary 100, canary 0, `mirrored: true`, and no `stepWeights`
entry is consumed. The next cycle shifts the first step. Only the canary
strategy is covered; blue/green mirroring is unchanged.

### Promotion

`NextPromotionWeight` keeps the existing semantics: `stepWeightPromotion: 0`
routes all traffic to primary in one cycle, a positive step moves
`stepWeightPromotion` per cycle, primary weight is capped at 100, canary weight
never goes negative, and reaching 100 finalises the promotion. `stepWeightPromotion`
above 100 is now a configuration error instead of a silent single jump.

### Event text

Existing event text is unchanged:

* `Advance %s.%s canary weight %v`
* `Advance %s.%s primary weight %v`
* `Routing all traffic to primary`
* `Copying %s.%s template spec to %s-primary.%s`

New text, which alerting rules should opt into explicitly:

* `Invalid analysis configuration: <reason>` (error)
* `Analysis configuration warning: <reason>` (warning)

The warnings are emitted once per rollout from the `Starting canary analysis`
branch, so a rollout that goes through N analysis cycles still emits each
warning once. This was chosen so that operator-side counters keyed on event text
see a stable count.

## Tests whose expectations changed

* `TestScheduler_ServicePromotion` (`scheduler_svc_test.go`): the
  `stepWeight: 20` / `maxWeight: 50` service canary now expects primary weights
  `100, 80, 60, 50` instead of `100, 80, 60, 40`. The old last step overshot
  `maxWeight` to a canary weight of 60; `maxWeight` is now honoured.
* `TestScheduler_ServicePromotionWithWeightsOverflow` (`scheduler_svc_test.go`):
  `stepWeights` containing `200` is no longer advanced to. The rollout is
  rejected and marked `Failed` with primary at 100 and canary at 0. Advancing
  would have written an unroutable weight.
* `TestWebhooks/canary` (`events_test.go`): with
  `stepWeight: 100` / `maxWeight: 50` / `stepWeightPromotion: 50`, the first
  analysis step now stops at `maxWeight` (50, not 100), and the promotion step
  of 50 takes primary from 50 to 100 in a single cycle. The expected event list
  and phase sequence were updated accordingly, and it now includes the
  `Analysis configuration warning` for `stepWeight 100` exceeding `maxWeight 50`.

Each change follows from `maxWeight` becoming a true ceiling; none of them
alters failure counting, rollback triggers or the routing calls themselves.

## Verification

* `pkg/controller/weight_test.go` — table-driven tests for
  `EffectiveMaxWeight`, `NextAnalysisWeight`, `NextPromotionWeight` and
  `ValidateWeightConfig`, plus full expected weight sequences
  (`TestWeightSequence`) so each scenario asserts every step, not just the last.
* `TestWeightProgressionProperty` — fixed seed (`20251125`), 500 generated valid
  configurations driven to promotion. Asserts the weights sum to 100, the canary
  weight is monotonically non-decreasing, never exceeds `EffectiveMaxWeight`,
  at most one mirror step occurs, and the number of advance steps is bounded by
  the configuration.
* `pkg/controller/scheduler_weight_fixture_test.go` — reuses
  `newDeploymentFixture`, the existing fake router and `record.FakeRecorder`.
  Checks the routed weights per cycle for `stepWeight`, `stepWeights` and mirror
  starts, both promotion modes, the rejection path, and asserts the exact
  `Advance` / warning event text, count and order.
