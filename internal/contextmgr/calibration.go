package contextmgr

import "math"

const (
	// calibrationMinRatio floors the correction ratio. 0.2 (not the former
	// 0.5) so a genuine, large overestimate - e.g. a reasoning-replay
	// provider's history inflated by ReasoningContent the provider never
	// actually bills - can still be corrected down instead of pinning at a
	// floor that keeps the overestimate permanent (see
	// ContextAccountingProfile, which fixes the estimate itself; this floor
	// bounds the EWMA correction for whatever the estimate still misses).
	// 0.2 keeps a floor at all: a ratio of exactly 0 would zero out every
	// future estimate outright on one bad sample.
	calibrationMinRatio     = 0.2
	calibrationMaxRatio     = 3.0
	defaultCalibrationAlpha = 0.2
)

// Calibration maintains a per-binding (provider+model) rolling correction
// ratio between estimated and provider-reported token usage. It uses EWMA
// (exponentially weighted moving average) to smooth noise from individual
// requests while tracking systematic drift in the len(s)/4 heuristic.
//
// The zero value is valid and means unity (no correction): Ratio is 0.0,
// which applyCalibration and the loop's emission path treat as 1.0.
type Calibration struct {
	// Alpha is the smoothing factor (0 < alpha <= 1). Higher values
	// react faster to changes. Defaults to 0.2.
	Alpha float64
	// Ratio is the current correction factor: reported/estimated.
	// Bounded to [calibrationMinRatio, calibrationMaxRatio] once samples
	// exist. The zero value is 0.0, which means no correction (treated as
	// 1.0 everywhere it is applied).
	Ratio float64
	// Samples is the number of updates applied.
	Samples int
}

// Update incorporates a new (estimated, reported) observation into the EWMA.
// If estimated is zero, the update is skipped to avoid division by zero.
func (c *Calibration) Update(estimated, reported int) {
	if estimated <= 0 {
		return
	}
	ratio := float64(reported) / float64(estimated)
	ratio = math.Max(calibrationMinRatio, math.Min(calibrationMaxRatio, ratio))

	if c.Samples == 0 {
		c.Ratio = ratio
		c.Samples = 1
		return
	}

	alpha := c.Alpha
	if !(alpha > 0 && alpha <= 1) {
		// Enforce the documented invariant (0 < alpha <= 1): fall back to
		// the default for alpha <= 0, alpha > 1, and NaN/+Inf/-Inf.
		alpha = defaultCalibrationAlpha
	}
	c.Ratio = math.Max(calibrationMinRatio, math.Min(calibrationMaxRatio, alpha*ratio+(1-alpha)*c.Ratio))
	c.Samples++
}

// Apply scales an estimated token count by this calibration's correction
// ratio, exactly as the planner does when it scores a history against the
// compaction trigger.
//
// Every surface that reports "how full is the context" must go through this
// method. The planner compares a CALIBRATED cost against the trigger
// (planner.go's Plan), so a gauge that divides an UNCALIBRATED estimate by
// the same budget is measuring the same history with a different ruler: with
// a ratio below 1.0 the displayed percentage runs ahead of the trigger and
// can sit far above 100% while the planner correctly sees a history below
// the threshold and never compacts. That disagreement is what this method
// exists to make impossible.
//
// A calibration with no samples applies no correction, matching
// PlanInput.CalibrationRatio, which callers leave unset until the first
// estimate-vs-actual observation lands.
func (c Calibration) Apply(estimated int) int {
	if c.Samples == 0 {
		return estimated
	}
	return applyCalibration(estimated, c.Ratio)
}

// applyCalibration scales an estimated token count by the calibration ratio.
// A ratio of 0 is treated as 1.0 (no correction) for backward compatibility.
func applyCalibration(estimated int, ratio float64) int {
	if ratio <= 0 {
		return estimated
	}
	return int(math.Round(float64(estimated) * ratio))
}
