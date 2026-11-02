package anim

import (
	"math"
	"testing"
	"time"
)

type pinnedBezier struct {
	points [4]float64
	want   []float64
}

type pinnedSpring struct {
	params [3]float64
	settle time.Duration
	want   []float64
}

type pinnedFollower struct {
	params, state [3]float64
	settle        time.Duration
	want          [][2]float64
}

var pinnedBeziers = []pinnedBezier{
	{[4]float64{0.25, 0.1, 0.25, 1}, []float64{0x1.8449218d3f7bp-04, 0x1.2e5487e009e2ap-02, 0x1.06d13ea9d7e3bp-01, 0x1.5d75f2ff0e457p-01, 0x1.9ad49de8b3eb8p-01, 0x1.c53cc6aee7fa8p-01, 0x1.e1abe64749941p-01, 0x1.f3852aa71a6ap-01, 0x1.fd170ca18dfbep-01}},
	{[4]float64{0.42, 0, 1, 1}, []float64{0x1.16f6c6e75fc6fp-06, 0x1.fe36d236ef798p-05, 0x1.095f8a71381eep-03, 0x1.b80903090c17dp-03, 0x1.42ece571226bep-02, 0x1.b76b2c3cc76bfp-02, 0x1.1c1095ba250e8p-01, 0x1.621dd7c882f9fp-01, 0x1.adc97c91cd91ap-01}},
	{[4]float64{0, 0, 0.58, 1}, []float64{0x1.48da0db8c9b9cp-03, 0x1.3bc4506efa0c7p-02, 0x1.c7ded48bb5e2cp-02, 0x1.244a69e19c4a2p-01, 0x1.5e898d476eca1p-01, 0x1.91fdbf3dbcfap-01, 0x1.bda81d63b1f84p-01, 0x1.e01c92dc91086p-01, 0x1.f74849c8c501dp-01}},
	{[4]float64{0.42, 0, 0.58, 1}, []float64{0x1.4321f73f28f7ep-06, 0x1.4e7a90c0eded8p-04, 0x1.7fc96cd5a0f0ep-03, 0x1.53d95d7a1376dp-02, 0x1p-01, 0x1.56135142f644ap-01, 0x1.a00da4ca97c3dp-01, 0x1.d630ade7e2426p-01, 0x1.f5e6f04606b83p-01}},
	{[4]float64{0.05, 0.9, 0.1, 1.1}, []float64{0x1.78976e906f27dp-01, 0x1.ca2bbc56b4bp-01, 0x1.edb0b6bc515bp-01, 0x1.ff832a73fa4a1p-01, 0x1.0421946f8515ep+00, 0x1.05d3eba478163p+00, 0x1.05cdfde641ffbp+00, 0x1.049d783944af4p+00, 0x1.029ab5778ab05p+00}},
	{[4]float64{0.9, -0.5, 0.1, 1.5}, []float64{-0x1.8ba60647e6e8dp-05, -0x1.3c7590e65a853p-04, -0x1.2d90450b20cedp-04, 0x1.1839a3601d958p-10, 0x1p-01, 0x1.ff73e32e4ff16p-01, 0x1.12d90450b20cep+00, 0x1.13c7590e65a85p+00, 0x1.0c5d30323f375p+00}},
	{[4]float64{0, 1, 1, 0}, []float64{0x1.8cb2ab32a394bp-02, 0x1.d87f0e12934d9p-02, 0x1.f586ea4cd5d05p-02, 0x1.fec3a7220f08fp-02, 0x1p-01, 0x1.009e2c6ef87b8p-01, 0x1.053c8ad99517ep-01, 0x1.13c078f6b6594p-01, 0x1.39a6aa66ae35bp-01}},
}

var pinnedSprings = []pinnedSpring{
	{[3]float64{200, 10, 1}, 1855773924, []float64{0x1.a2076108bcp-14, 0x1.9808572bd72p-08, 0x1.8c404478f3d8p-06, 0x1.a3150a75aa4f4p-03, 0x1.4202a5c00dc84p-01, 0x1.41e65e3bf822p+00, 0x1.efff54e6986f8p-01, 0x1.0380e84a51aa4p+00}},
	{[3]float64{300, 30, 1}, 661010741, []float64{0x1.3771584ba7p-13, 0x1.2263c8156908p-07, 0x1.0c145de2866p-05, 0x1.d2c94eadba8c4p-03, 0x1.1f41198faf8acp-01, 0x1.d8838cd8770bap-01, 0x1.00f34034c26b7p+00, 0x1p+00}},
	{[3]float64{100, 20, 1}, 1176452636, []float64{0x1.a0a5081f58p-15, 0x1.8db7c0d4d7p-09, 0x1.7943c30ad734p-07, 0x1.7179c2ac4f788p-04, 0x1.0e95393a6219p-02, 0x1.3020005305ea7p-01, 0x1.d11ca9b3ace7ap-01, 0x1.fe7445adb6dd3p-01}},
	{[3]float64{100, 60, 1}, 5402526854, []float64{0x1.9b29a79838p-15, 0x1.6779537c338p-09, 0x1.377d19141798p-07, 0x1.cae72c0749eap-05, 0x1.0ec001c779fbp-03, 0x1.1366605da8502p-02, 0x1.ecd78d5db44c6p-02, 0x1.7a4c08dcc2879p-01}},
	{[3]float64{400, 28, 2}, 1336669921, []float64{0x1.a179127b0ap-14, 0x1.93c5d316076p-08, 0x1.8427ea7e99e6p-06, 0x1.8ace6b1ca204cp-03, 0x1.2249eeadb07a2p-01, 0x1.1a351706edbe2p+00, 0x1.058c97d83d7f4p+00, 0x1.011528594f243p+00}},
	{[3]float64{800, 10, 0.5}, 924682616, []float64{0x1.a097b7b14c8p-11, 0x1.8a9481a11b9bp-05, 0x1.6d99a88aca024p-03, 0x1.1215c2e3d6f6ep+00, 0x1.565501aea0993p+00, 0x1.e6be45742b3c1p-01, 0x1.0451fff76de77p+00, 0x1.ffdcd3258f83p-01}},
}

var pinnedFollowers = []pinnedFollower{
	{[3]float64{200, 10, 1}, [3]float64{0, 0, 100}, 1262512206, [][2]float64{{0x1.4695c3ced2p-07, 0x1.3e650b7e8d5c9p+04}, {0x1.3592357e7e82p+01, 0x1.253250059ad19p+08}, {0x1.f72422fc1588dp+05, 0x1.bc79bb6ce17d8p+09}, {0x1.837f7a5427172p+06, -0x1.567df7f51158dp+07}}},
	{[3]float64{200, 10, 1}, [3]float64{300, -1500, 20}, 1409912109, [][2]float64{{0x1.2a7ac8451f9dp+08, -0x1.812944c7d12b3p+10}, {0x1.0f3d073a699f1p+08, -0x1.0218cd05a21e8p+11}, {0x1.c9d589eeb51d7p+05, -0x1.295eb7db1e844p+11}, {0x1.4cc2508bdd7f9p+05, 0x1.302bf5b05df82p+08}}},
	{[3]float64{200, 10, 1}, [3]float64{-40, 700, 640}, 1634216308, [][2]float64{{-0x1.39e2cb075df3p+05, 0x1.9e243c9f27701p+09}, {-0x1.a95955acec2cp+03, 0x1.41c603da1aa87p+11}, {0x1.a2c88474eee38p+08, 0x1.7697c532cc9fbp+12}, {0x1.32605fbad350ap+09, -0x1.0eaa223c00b01p+10}}},
	{[3]float64{100, 20, 1}, [3]float64{0, 0, 100}, 821228027, [][2]float64{{0x1.4580ee588p-08, 0x1.3cd0e1e3b421fp+03}, {0x1.26bcf060782p+00, 0x1.10af9e835fcf2p+07}, {0x1.a6c9296b3947p+04, 0x1.6fe1230e7e19dp+08}, {0x1.6b5e64945f14fp+06, 0x1.250cdb5d07586p+06}}},
	{[3]float64{100, 20, 1}, [3]float64{300, -1500, 20}, 863342284, [][2]float64{{0x1.2a8042bb20301p+08, -0x1.767c81688d85bp+10}, {0x1.1453297b7de69p+08, -0x1.6bdd8ab74d72p+10}, {0x1.55a940961fb9p+07, -0x1.0183fef08b787p+10}, {0x1.153857becfbcfp+05, -0x1.eadbef6f05e74p+06}}},
	{[3]float64{100, 20, 1}, [3]float64{-40, 700, 640}, 1023559570, [][2]float64{{-0x1.3a2f7ebaa59p+05, 0x1.78b6c6325a96cp+09}, {-0x1.6a083bf3d4bep+04, 0x1.650c59f404a66p+10}, {0x1.4adefc980c948p+07, 0x1.38b290ff84c92p+11}, {0x1.236d77697d5d2p+09, 0x1.cbb8f82056b91p+08}}},
	{[3]float64{100, 60, 1}, [3]float64{0, 0, 100}, 3526611327, [][2]float64{{0x1.41388aeeecp-08, 0x1.36957f962f17p+03}, {0x1.e6b3772f64d8p-01, 0x1.99ad912f56a5ap+06}, {0x1.a70c02c7ae98p+03, 0x1.28c5782b3aeecp+07}, {0x1.8108667134dbap+05, 0x1.63fc4e1c4e9e7p+06}}},
	{[3]float64{100, 60, 1}, [3]float64{300, -1500, 20}, 4092407226, [][2]float64{{0x1.2a87c92ef9f74p+08, -0x1.67f00da44a50fp+10}, {0x1.19f9b5b42a663p+08, -0x1.a95ec1af91848p+09}, {0x1.e1737a49c568p+07, -0x1.7db49d5f95dd3p+08}, {0x1.2fc7326ea1e73p+07, -0x1.c4925a7fb9428p+07}}},
	{[3]float64{100, 60, 1}, [3]float64{-40, 700, 640}, 4623413085, [][2]float64{{-0x1.3a4c53082b2p+05, 0x1.6a99ae829a0afp+09}, {-0x1.a5ddbea95062p+04, 0x1.dfd17b853604ep+09}, {0x1.e24697036764p+05, 0x1.f0a17101759cp+09}, {0x1.2581f49a52252p+08, 0x1.293e52f7ac068p+09}}},
}

var pinnedSpringTimes = []time.Duration{
	time.Millisecond, 8 * time.Millisecond, 16 * time.Millisecond, 50 * time.Millisecond,
	100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond,
}

var pinnedFollowerTimes = []time.Duration{
	time.Millisecond, 16 * time.Millisecond, 100 * time.Millisecond, 400 * time.Millisecond,
}

func TestBezierHoldsItsPinnedValues(t *testing.T) {
	for _, c := range pinnedBeziers {
		b, err := NewBezier(c.points[0], c.points[1], c.points[2], c.points[3])
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range c.want {
			x := float64(i+1) / 10
			if got := b.Ease(x); math.Abs(got-want) > 1e-15 {
				t.Errorf("%v at %v = %x, want %x", c.points, x, got, want)
			}
		}
	}
}

func TestSpringHoldsItsPinnedValues(t *testing.T) {
	for _, c := range pinnedSprings {
		s, err := NewSpring(c.params[0], c.params[1], c.params[2])
		if err != nil {
			t.Fatal(err)
		}
		if s.Duration() != c.settle {
			t.Errorf("%v settles after %v, want %v", c.params, s.Duration(), c.settle)
		}
		for i, want := range c.want {
			if got := s.At(pinnedSpringTimes[i]); math.Abs(got-want) > 1e-15 {
				t.Errorf("%v at %v = %x, want %x", c.params, pinnedSpringTimes[i], got, want)
			}
		}
	}
}

func TestFollowerHoldsItsPinnedValues(t *testing.T) {
	for _, c := range pinnedFollowers {
		s, err := NewSpring(c.params[0], c.params[1], c.params[2])
		if err != nil {
			t.Fatal(err)
		}
		f := newFollower(s, c.state[0], c.state[1], c.state[2], 0)
		if f.settle != c.settle {
			t.Errorf("%v from %v settles after %v, want %v", c.params, c.state, f.settle, c.settle)
		}
		for i, want := range c.want {
			x, v := f.at(pinnedFollowerTimes[i])
			if p := f.position(pinnedFollowerTimes[i]); p != want[0] {
				t.Errorf("%v from %v at %v: position %x, want %x", c.params, c.state, pinnedFollowerTimes[i], p, want[0])
			}
			if x != want[0] || v != want[1] {
				t.Errorf("%v from %v at %v = (%x, %x), want %x", c.params, c.state, pinnedFollowerTimes[i], x, v, want)
			}
		}
	}
}
