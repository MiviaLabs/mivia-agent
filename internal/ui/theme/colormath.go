package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// srgb is a colour in sRGB space, each channel in [0,1].
type srgb struct{ r, g, b float64 }

func parseHex(hex string) (srgb, error) {
	h := strings.TrimPrefix(hex, "#")
	if len(h) != 6 {
		return srgb{}, fmt.Errorf("theme: invalid hex colour %q", hex)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return srgb{}, fmt.Errorf("theme: invalid hex colour %q: %w", hex, err)
	}
	return srgb{
		r: float64((v>>16)&0xff) / 255,
		g: float64((v>>8)&0xff) / 255,
		b: float64(v&0xff) / 255,
	}, nil
}

func (c srgb) hex() string {
	clamp := func(v float64) int {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		return int(math.Round(v * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", clamp(c.r), clamp(c.g), clamp(c.b))
}

func srgbChannelToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearChannelToSRGB(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

func (c srgb) linear() (r, g, b float64) {
	return srgbChannelToLinear(c.r), srgbChannelToLinear(c.g), srgbChannelToLinear(c.b)
}

func linearToSRGBColor(r, g, b float64) srgb {
	return srgb{linearChannelToSRGB(r), linearChannelToSRGB(g), linearChannelToSRGB(b)}
}

// relativeLuminance is the WCAG 2.1 relative luminance of a hex colour.
// https://www.w3.org/TR/WCAG21/#dfn-relative-luminance
func relativeLuminance(hex string) (float64, error) {
	c, err := parseHex(hex)
	if err != nil {
		return 0, err
	}
	r, g, b := c.linear()
	return 0.2126*r + 0.7152*g + 0.0722*b, nil
}

// contrastRatio is the WCAG 2.1 contrast ratio between two hex colours,
// always >= 1.
func contrastRatio(hexA, hexB string) (float64, error) {
	la, err := relativeLuminance(hexA)
	if err != nil {
		return 0, err
	}
	lb, err := relativeLuminance(hexB)
	if err != nil {
		return 0, err
	}
	lighter, darker := la, lb
	if darker > lighter {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05), nil
}

// hslToSRGB converts HSL (h in degrees [0,360), s and l in [0,1]) to
// sRGB. Standard conversion, used by the palette search in search.go to
// sample colours within a hue window.
func hslToSRGB(h, s, l float64) srgb {
	if s == 0 {
		return srgb{l, l, l}
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	hk := h / 360
	f := func(t float64) float64 {
		for t < 0 {
			t++
		}
		for t > 1 {
			t--
		}
		switch {
		case t < 1.0/6.0:
			return p + (q-p)*6*t
		case t < 1.0/2.0:
			return q
		case t < 2.0/3.0:
			return p + (q-p)*(2.0/3.0-t)*6
		default:
			return p
		}
	}
	return srgb{f(hk + 1.0/3.0), f(hk), f(hk - 1.0/3.0)}
}

// --- 3x3 matrix helpers, used by the LMS dichromat pipeline in cvd.go ---

type mat3 [3][3]float64

func (m mat3) mulVec(v [3]float64) [3]float64 {
	return [3]float64{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}

// inverse computes the inverse of a 3x3 matrix by cofactor expansion.
// Used to derive the LMS->linear-RGB matrix from the forward
// Hunt-Pointer-Estevez matrix rather than hand-transcribing a second set
// of magic numbers that could silently drift from the forward one.
func (m mat3) inverse() (mat3, error) {
	det := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if math.Abs(det) < 1e-12 {
		return mat3{}, fmt.Errorf("theme: singular matrix")
	}
	invDet := 1 / det
	var out mat3
	out[0][0] = (m[1][1]*m[2][2] - m[1][2]*m[2][1]) * invDet
	out[0][1] = (m[0][2]*m[2][1] - m[0][1]*m[2][2]) * invDet
	out[0][2] = (m[0][1]*m[1][2] - m[0][2]*m[1][1]) * invDet
	out[1][0] = (m[1][2]*m[2][0] - m[1][0]*m[2][2]) * invDet
	out[1][1] = (m[0][0]*m[2][2] - m[0][2]*m[2][0]) * invDet
	out[1][2] = (m[0][2]*m[1][0] - m[0][0]*m[1][2]) * invDet
	out[2][0] = (m[1][0]*m[2][1] - m[1][1]*m[2][0]) * invDet
	out[2][1] = (m[0][1]*m[2][0] - m[0][0]*m[2][1]) * invDet
	out[2][2] = (m[0][0]*m[1][1] - m[0][1]*m[1][0]) * invDet
	return out, nil
}

// --- CIE XYZ / CIELAB, for CIE76 dE separation ---

// sRGB (D65) linear-RGB -> XYZ, IEC 61966-2-1.
var rgbToXYZMat = mat3{
	{0.4124564, 0.3575761, 0.1804375},
	{0.2126729, 0.7151522, 0.0721750},
	{0.0193339, 0.1191920, 0.9503041},
}

// D65 white point.
const (
	whiteX = 0.95047
	whiteY = 1.00000
	whiteZ = 1.08883
)

func labF(t float64) float64 {
	const delta = 6.0 / 29.0
	if t > delta*delta*delta {
		return math.Cbrt(t)
	}
	return t/(3*delta*delta) + 4.0/29.0
}

// xyzToLab converts CIE XYZ (D65) to CIELAB.
func xyzToLab(x, y, z float64) (l, a, b float64) {
	fx := labF(x / whiteX)
	fy := labF(y / whiteY)
	fz := labF(z / whiteZ)
	l = 116*fy - 16
	a = 500 * (fx - fy)
	b = 200 * (fy - fz)
	return
}

// labFromHex converts a hex sRGB colour to CIELAB.
func labFromHex(hex string) (l, a, b float64, err error) {
	c, err := parseHex(hex)
	if err != nil {
		return 0, 0, 0, err
	}
	r, g, bl := c.linear()
	xyz := rgbToXYZMat.mulVec([3]float64{r, g, bl})
	l, a, b = xyzToLab(xyz[0], xyz[1], xyz[2])
	return l, a, b, nil
}

// cie76 is the CIE76 colour-difference formula: euclidean distance in Lab.
func cie76(l1, a1, b1, l2, a2, b2 float64) float64 {
	return math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (b1-b2)*(b1-b2))
}
