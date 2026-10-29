package plan

import (
	"sync"

	eph "github.com/TuSKan/astrogo/ephemeris"
)

// defaultProvider is the [eph.Default] provider that the Sun, MoonSep and
// MoonIllum constraints and the Moon-vector cache use when the caller supplies
// none. It is built on first use and then shared.
//
// Each check used to build its own. That cost six allocations and a
// registration of Pluto's elements per check, and it threw away whatever the
// provider had just computed: a fresh provider cannot reuse the Earth series
// the previous check evaluated at the same instant.
//
// Sharing is safe because the Provider in ephemeris/kepler forbids only a
// Register concurrent with Locate, and [eph.Default] registers Pluto before it
// returns. Nothing in this package registers on this value, and it never reaches
// a caller: it is handed only to the constraints' own [Planet]s and to
// [MoonIllumination], which read through it.
var defaultProvider = sync.OnceValue(eph.Default)
