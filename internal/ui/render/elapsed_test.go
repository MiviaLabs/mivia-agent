package render

import "testing"

// TestFormatElapsed pins the single duration ladder (transcript-polish.md
// R5, wireframes-panes.md section 4): raw milliseconds under a second,
// one-decimal seconds to the rounding boundary, minutes past it. The
// boundary values are the ones a format change can quietly break.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		ms   int
		want string
	}{
		{-1, "0ms"},         // a negative clock floors at zero
		{-90000, "0ms"},     // negative does not reach the minutes rung
		{0, "0ms"},          //
		{1, "1ms"},          //
		{250, "250ms"},      //
		{999, "999ms"},      // top of the milliseconds rung
		{1000, "1.0s"},      // bottom of the seconds rung
		{4100, "4.1s"},      //
		{23500, "23.5s"},    //
		{59949, "59.9s"},    // last value %.1f does not round to 60.0s
		{59950, "0m 59s"},   // the minutes rung starts before a minute fits
		{60000, "1m 00s"},   //
		{90000, "1m 30s"},   //
		{600000, "10m 00s"}, //
	}
	for _, c := range cases {
		if got := FormatElapsed(c.ms); got != c.want {
			t.Errorf("FormatElapsed(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}
