package contextmgr

import "math"

const (
	calibrationMinRatio     = 0.5
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
	// Bounded to [0.5, 3.0] once samples exist. The zero value is 0.0,
	// which means no correction (treated as 1.0 everywhere it is applied).
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

// applyCalibration scales an estimated token count by the calibration ratio.
// A ratio of 0 is treated as 1.0 (no correction) for backward compatibility.
func applyCalibration(estimated int, ratio float64) int {
	if ratio <= 0 {
		return estimated
	}
	return int(math.Round(float64(estimated) * ratio))
}
