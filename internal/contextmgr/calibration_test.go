package contextmgr

import (
	"math"
	"testing"
)

func TestCalibrationZeroValue(t *testing.T) {
	var c Calibration
	if c.Ratio != 0 {
		t.Fatalf("zero-value Ratio should be 0, got %f", c.Ratio)
	}
	if c.Samples != 0 {
		t.Fatalf("zero-value Samples should be 0, got %d", c.Samples)
	}
	// applyCalibration treats 0 as 1.0
	if got := applyCalibration(100, 0); got != 100 {
		t.Fatalf("applyCalibration(100, 0) = %d, want 100", got)
	}
}

func TestCalibrationUpdateEWMA(t *testing.T) {
	c := Calibration{Alpha: 0.5}
	// First observation initializes directly
	c.Update(100, 200)
	if c.Ratio != 2.0 {
		t.Fatalf("first update: Ratio = %f, want 2.0", c.Ratio)
	}
	if c.Samples != 1 {
		t.Fatalf("first update: Samples = %d, want 1", c.Samples)
	}
	// Second observation blends: 0.5*1.5 + 0.5*2.0 = 1.75
	c.Update(100, 150)
	if c.Ratio != 1.75 {
		t.Fatalf("second update: Ratio = %f, want 1.75", c.Ratio)
	}
	if c.Samples != 2 {
		t.Fatalf("second update: Samples = %d, want 2", c.Samples)
	}
}

func TestCalibrationBounds(t *testing.T) {
	c := Calibration{Alpha: 1.0}
	// Observed ratio 5.0 should be clamped to 3.0
	c.Update(10, 50)
	if c.Ratio != calibrationMaxRatio {
		t.Fatalf("upper bound: Ratio = %f, want %f", c.Ratio, calibrationMaxRatio)
	}
	// Observed ratio 0.1 should be clamped to 0.5
	c.Update(100, 10)
	if c.Ratio != calibrationMinRatio {
		t.Fatalf("lower bound: Ratio = %f, want %f", c.Ratio, calibrationMinRatio)
	}
}

func TestCalibrationSkipZeroEstimate(t *testing.T) {
	c := Calibration{Alpha: 0.5}
	c.Update(100, 200) // ratio = 2.0
	c.Update(0, 50)    // should be skipped
	if c.Samples != 1 {
		t.Fatalf("Samples should remain 1 after zero-estimate update, got %d", c.Samples)
	}
	if c.Ratio != 2.0 {
		t.Fatalf("Ratio should remain 2.0, got %f", c.Ratio)
	}
}

func TestApplyCalibration(t *testing.T) {
	tests := []struct {
		estimated int
		ratio     float64
		want      int
	}{
		{100, 1.0, 100},
		{100, 2.0, 200},
		{100, 0.5, 50},
		{33, 1.5, 50}, // rounds 49.5 → 50
		{0, 2.0, 0},
		{100, 0, 100},  // zero ratio = no correction
		{100, -1, 100}, // negative ratio = no correction
	}
	for _, tt := range tests {
		got := applyCalibration(tt.estimated, tt.ratio)
		if got != tt.want {
			t.Errorf("applyCalibration(%d, %f) = %d, want %d", tt.estimated, tt.ratio, got, tt.want)
		}
	}
}

func TestApplyCalibrationGolden(t *testing.T) {
	// ratio=1.0 must be identity — golden tests depend on this
	if got := applyCalibration(42, 1.0); got != 42 {
		t.Fatalf("golden: applyCalibration(42, 1.0) = %d, want 42", got)
	}
	if got := applyCalibration(0, 1.0); got != 0 {
		t.Fatalf("golden: applyCalibration(0, 1.0) = %d, want 0", got)
	}
}

func TestApplyCalibrationNegInf(t *testing.T) {
	// math.Inf(-1) should be treated as 0
	ratio := math.Inf(-1)
	got := applyCalibration(100, ratio)
	if got != 100 {
		t.Fatalf("applyCalibration(100, -Inf) = %d, want 100", got)
	}
}

func TestCalibrationDefaultAlpha(t *testing.T) {
	// When Alpha is 0, Update should use defaultCalibrationAlpha (0.2)
	var c Calibration
	c.Update(100, 200) // first observation: ratio=2.0
	c.Alpha = 0        // reset to 0 to trigger default path
	c.Update(100, 100) // should use alpha=0.2: 0.2*1.0 + 0.8*2.0 = 1.8
	if c.Samples != 2 {
		t.Fatalf("Samples = %d, want 2", c.Samples)
	}
	// 0.2 * 1.0 + 0.8 * 2.0 = 1.8
	if math.Abs(c.Ratio-1.8) > 0.001 {
		t.Fatalf("Ratio = %f, want 1.8 (with default alpha 0.2)", c.Ratio)
	}
}
