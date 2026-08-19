package main

import (
	"strings"
	"testing"
)

func TestAvailableEffectsAreKnown(t *testing.T) {
	names := availableEffects()
	if len(names) == 0 {
		t.Fatal("aucun effet disponible")
	}
	for _, want := range []string{"echo"} {
		if _, ok := effectFilter(want, 1.0); !ok {
			t.Errorf("effet %q absent du catalogue", want)
		}
	}
}

func TestUnknownEffectIsRejected(t *testing.T) {
	if _, ok := effectFilter("dubstep", 1.0); ok {
		t.Error("effectFilter(\"dubstep\") = ok, want refusé")
	}
}

func TestForceModulatesTheProducedFilter(t *testing.T) {
	low, _ := effectFilter("echo", 0.5)
	high, _ := effectFilter("echo", 2.0)
	if low == high {
		t.Errorf("la force ne change rien au filtre echo : %q", low)
	}
}

func TestValidForceRejectsOutOfBoundsValues(t *testing.T) {
	for _, f := range []float64{-1, -0.1, 2.1, 10} {
		if validForce(f) {
			t.Errorf("validForce(%v) = true, want false", f)
		}
	}
	for _, f := range []float64{0, 0.5, 1, 2} {
		if !validForce(f) {
			t.Errorf("validForce(%v) = false, want true", f)
		}
	}
}

func TestAudioFiltersPlaceEffectsAfterTempo(t *testing.T) {
	got := audioFilters(24000, 1.5, 1.0, []Effect{{Name: "echo", Force: 1}})
	if i, j := strings.Index(got, "atempo"), strings.Index(got, "aecho"); i < 0 || j < 0 || i > j {
		t.Errorf("audioFilters = %q : atempo doit précéder les effets", got)
	}
}

func TestZeroForceMeansNominalForce(t *testing.T) {
	// 0 = « non renseigné » dans le JSON, on retombe sur 1.0.
	withZero := audioFilters(24000, 1, 1, []Effect{{Name: "echo", Force: 0}})
	withOne := audioFilters(24000, 1, 1, []Effect{{Name: "echo", Force: 1}})
	if withZero != withOne {
		t.Errorf("force 0 = %q, force 1 = %q : elles devraient coïncider", withZero, withOne)
	}
}
