package plan

import (
	"math"
	"sync"
	"testing"

	"github.com/TuSKan/astrogo/angle"
	"github.com/TuSKan/astrogo/coord"
	eph "github.com/TuSKan/astrogo/ephemeris"
	"github.com/TuSKan/astrogo/time"
)

// dpFixture is what the tests below share: a site, a target, and a spread of
// epochs, each with the Context a scheduler would build for it. The epochs run
// at an irregular step across one night and then across the span the built-in
// ephemeris covers, so a provider that kept anything from one call to the next
// would be asked about instants it has and has not seen.
type dpFixture struct {
	site   *Site
	target Observable
	times  []time.Time
	ctxs   []*coord.Context
}

func newDPFixture(t *testing.T) dpFixture {
	t.Helper()

	loc, err := coord.NewGeodetic(angle.Deg(-70.4), angle.Deg(-24.6), 2635)
	if err != nil {
		t.Fatal(err)
	}

	site, err := NewSite("Paranal", loc)
	if err != nil {
		t.Fatal(err)
	}

	f := dpFixture{site: site, target: NewStar("T", angle.Hour(5.5), angle.Deg(-5))}

	for i := range 40 {
		f.times = append(f.times, time.FromJD(2460000.5+float64(i)*0.0257, time.UTC))
	}

	for _, jd := range []float64{2415020.5, 2451545.0, 2469807.5} {
		f.times = append(f.times, time.FromJD(jd, time.UTC))
	}

	for _, tm := range f.times {
		f.ctxs = append(f.ctxs, coord.NewContext(tm, site.Location(), site.Refraction()))
	}

	return f
}

// dpAnswer is one epoch's three constraint values.
type dpAnswer struct{ sun, sep, illum float64 }

// dpReference computes the three values the way the constraints did before they
// shared a provider: each from an eph.Default() built for the purpose.
func dpReference(t *testing.T, f dpFixture, i int) dpAnswer {
	t.Helper()

	tm, ctx := f.times[i], f.ctxs[i]

	aa, err := skyAltAzCtx(NewSun(eph.Default()), tm, ctx)
	if err != nil {
		t.Fatal(err)
	}

	pos, err := topocentricPosition(f.target, tm, ctx)
	if err != nil {
		t.Fatal(err)
	}

	moon, err := topocentricPosition(NewMoon(eph.Default()), tm, ctx)
	if err != nil {
		t.Fatal(err)
	}

	frac, _, err := MoonIllumination(tm, eph.Default())
	if err != nil {
		t.Fatal(err)
	}

	return dpAnswer{sun: aa.Alt().Degrees(), sep: coord.Separation(pos, moon).Degrees(), illum: frac}
}

// dpChecked runs the three constraints under test at epoch i.
func dpChecked(f dpFixture, i int) (dpAnswer, error) {
	tm, ctx := f.times[i], f.ctxs[i]

	sun, err := Sun{Threshold: angle.Deg(-18)}.CheckCtx(f.target, tm, f.site, ctx)
	if err != nil {
		return dpAnswer{}, err
	}

	sep, err := MoonSep{Threshold: angle.Deg(30)}.CheckCtx(f.target, tm, f.site, ctx)
	if err != nil {
		return dpAnswer{}, err
	}

	illum, err := MoonIllum{Threshold: 0.5}.CheckCtx(f.target, tm, f.site, ctx)
	if err != nil {
		return dpAnswer{}, err
	}

	return dpAnswer{sun: sun.Value, sep: sep.Value, illum: illum.Value}, nil
}

func (a dpAnswer) sameBits(b dpAnswer) bool {
	return math.Float64bits(a.sun) == math.Float64bits(b.sun) &&
		math.Float64bits(a.sep) == math.Float64bits(b.sep) &&
		math.Float64bits(a.illum) == math.Float64bits(b.illum)
}

// TestDefaultProviderIsShared pins the point of the change: the fallback
// provider is built once, not once per call.
func TestDefaultProviderIsShared(t *testing.T) {
	if defaultProvider() != defaultProvider() {
		t.Fatal("defaultProvider returned two different providers")
	}
}

// TestSharedProviderConstraintsAreBitIdentical is the guarantee that sharing
// changes nothing a caller can see: Sun, MoonSep and MoonIllum return, bit for
// bit, what they returned when each check built its own provider.
func TestSharedProviderConstraintsAreBitIdentical(t *testing.T) {
	f := newDPFixture(t)

	// Twice over the same epochs, so a provider that remembered the last
	// instant it evaluated is asked again about instants it has seen.
	for pass := range 2 {
		for i := range f.times {
			want := dpReference(t, f, i)

			got, err := dpChecked(f, i)
			if err != nil {
				t.Fatalf("pass %d, epoch %d: %v", pass, i, err)
			}

			if !got.sameBits(want) {
				t.Errorf("pass %d, epoch %d: got %+v, want %+v", pass, i, got, want)
			}
		}
	}
}

// TestSharedProviderIsSafeForConcurrentChecks runs the constraints from many
// goroutines at once over one provider. Under -race it is the check that none of
// them writes to it; the comparison is the check that none of them is confused
// by another.
func TestSharedProviderIsSafeForConcurrentChecks(t *testing.T) {
	f := newDPFixture(t)

	want := make([]dpAnswer, len(f.times))
	for i := range f.times {
		want[i] = dpReference(t, f, i)
	}

	var wg sync.WaitGroup

	for g := range 8 {
		wg.Go(func() {
			// Each goroutine starts at its own epoch, so they overlap.
			for k := range 2 * len(f.times) {
				i := (g*5 + k) % len(f.times)

				got, err := dpChecked(f, i)
				if err != nil {
					t.Errorf("goroutine %d, epoch %d: %v", g, i, err)

					return
				}

				if !got.sameBits(want[i]) {
					t.Errorf("goroutine %d, epoch %d: got %+v, want %+v", g, i, got, want[i])

					return
				}
			}
		})
	}

	wg.Wait()
}

// TestConstraintChecksDoNotBuildAProvider states why a check must not
// construct the fallback provider: building one costs more allocations than the
// check around it should ever make. The bound is the cost of eph.Default()
// itself, so it needs no magic number and follows the provider if it grows.
func TestConstraintChecksDoNotBuildAProvider(t *testing.T) {
	f := newDPFixture(t)
	tm, ctx := f.times[0], f.ctxs[0]

	build := testing.AllocsPerRun(20, func() { _ = eph.Default() })
	if build < 2 {
		t.Fatalf("eph.Default allocates %v times; this test measures nothing", build)
	}

	checks := map[string]func() error{
		"Sun": func() error {
			_, err := Sun{Threshold: angle.Deg(-18)}.CheckCtx(f.target, tm, f.site, ctx)

			return err
		},
		"MoonSep": func() error {
			_, err := MoonSep{Threshold: angle.Deg(30)}.CheckCtx(f.target, tm, f.site, ctx)

			return err
		},
		"MoonIllum": func() error {
			_, err := MoonIllum{Threshold: 0.5}.CheckCtx(f.target, tm, f.site, ctx)

			return err
		},
	}

	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); err != nil { // the first call may build the shared provider
				t.Fatal(err)
			}

			var err error

			allocs := testing.AllocsPerRun(20, func() { err = check() })

			if err != nil {
				t.Fatal(err)
			}

			if allocs >= build {
				t.Errorf("a %s check allocates %v times, no fewer than the %v of building a provider", name, allocs, build)
			}
		})
	}
}
