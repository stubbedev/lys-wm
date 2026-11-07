package compositor

import (
	"math"
	"testing"
)

func TestAClientRampComposesAfterTheProfilesInverseCurve(t *testing.T) {
	const n = 256
	encode := make([]float64, n)
	for i := range encode {
		encode[i] = math.Pow(float64(i)/(n-1), 1/2.4)
	}
	client := make([]uint16, 3*n)
	for ch := range 3 {
		for i := range n {
			client[ch*n+i] = uint16(math.Round(math.Pow(float64(i)/(n-1), 1.1+0.1*float64(ch)) * math.MaxUint16))
		}
	}
	got := composeGamma(encode, client)
	for ch := range 3 {
		for i, e := range encode {
			want := math.Pow(e, 1.1+0.1*float64(ch)) * math.MaxUint16
			if math.Abs(float64(got[ch*n+i])-want) > math.MaxUint16/255 {
				t.Fatalf("channel %d entry %d: %d, want ramp(inverse TRC) = %.0f within a code value", ch, i, got[ch*n+i], want)
			}
		}
	}
}
