package contextmgr

import (
	"math"
	"testing"
)

// fuzzCalibrationAlphas are the seeded alpha values for
// FuzzCalibrationInvariants: in-contract values (0.5, 1.0), the zero value
// (default path), and out-of-contract values that must fall back to the
// documented default (2.0, NaN, +Inf, -Inf).
func fuzzCalibrationAlphas() []float64 {
	return []float64{2.0, 0, 0.5, 1.0, math.NaN(), math.Inf(1), math.Inf(-1)}
}

// FuzzCalibrationInvariants asserts the calibration postconditions on every
// input: no panic, a finite Ratio within [calibrationMinRatio,
// calibrationMaxRatio] whenever a sample exists, a zero estimate never
// increments Samples, and applyCalibration of a positive estimate stays
// finite and non-negative.
func FuzzCalibrationInvariants(f *testing.F) {
	for _, alpha := range fuzzCalibrationAlphas() {
		f.Add(alpha, int64(100), int64(200), int64(100), int64(50))
		f.Add(alpha, int64(0), int64(1), int64(0), int64(1))
		f.Add(alpha, int64(1), int64(1), int64(0), int64(0))
	}
	f.Fuzz(func(t *testing.T, alpha float64, e1, r1, e2, r2 int64) {
		c := Calibration{Alpha: alpha}
		c.Update(int(e1), int(r1))
		if e1 <= 0 && c.Samples != 0 {
			t.Fatalf("zero estimate incremented Samples: got %d", c.Samples)
		}
		assertCalibrationInvariants(t, &c)
		before := c.Samples
		c.Update(int(e2), int(r2))
		if e2 <= 0 && c.Samples != before {
			t.Fatalf("zero estimate incremented Samples: got %d, want %d", c.Samples, before)
		}
		assertCalibrationInvariants(t, &c)
	})
}

// assertCalibrationInvariants checks the postconditions shared by every
// update path: with samples present the Ratio must be finite and within the
// documented bounds, and applying the ratio to a positive estimate must stay
// finite and non-negative (a NaN ratio would surface here as a huge negative
// int on amd64 conversion).
func assertCalibrationInvariants(t *testing.T, c *Calibration) {
	t.Helper()
	if c.Samples > 0 {
		if math.IsNaN(c.Ratio) || math.IsInf(c.Ratio, 0) {
			t.Fatalf("Ratio %v is not finite with Samples=%d", c.Ratio, c.Samples)
		}
		if c.Ratio < calibrationMinRatio || c.Ratio > calibrationMaxRatio {
			t.Fatalf("Ratio %v out of [%v, %v] with Samples=%d", c.Ratio, calibrationMinRatio, calibrationMaxRatio, c.Samples)
		}
	}
	if got := applyCalibration(100, c.Ratio); got < 0 {
		t.Fatalf("applyCalibration(100, %v) = %d < 0", c.Ratio, got)
	}
}
