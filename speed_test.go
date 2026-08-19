package main

import (
	"strings"
	"testing"
)

func TestAtempoChainIsEmptyAtNeutralFactor(t *testing.T) {
	if got := atempoChain(1.0); len(got) != 0 {
		t.Errorf("atempoChain(1.0) = %v, want vide", got)
	}
}

func TestAtempoChainUsesASingleFilterWithinFfmpegRange(t *testing.T) {
	cases := map[float64]string{
		1.5: "atempo=1.5",
		0.8: "atempo=0.8",
		2.0: "atempo=2",
		0.5: "atempo=0.5",
	}
	for factor, want := range cases {
		got := strings.Join(atempoChain(factor), ",")
		if got != want {
			t.Errorf("atempoChain(%v) = %q, want %q", factor, got, want)
		}
	}
}

func TestAtempoChainCascadesOutsideFfmpegRange(t *testing.T) {
	// ffmpeg n'accepte qu'un facteur entre 0.5 et 2 par filtre atempo.
	cases := map[float64]string{
		3.0:  "atempo=2,atempo=1.5",
		6.0:  "atempo=2,atempo=2,atempo=1.5",
		0.25: "atempo=0.5,atempo=0.5",
	}
	for factor, want := range cases {
		got := strings.Join(atempoChain(factor), ",")
		if got != want {
			t.Errorf("atempoChain(%v) = %q, want %q", factor, got, want)
		}
	}
}

func TestAtempoChainStaysWithinFfmpegBounds(t *testing.T) {
	for _, factor := range []float64{0.25, 0.3, 0.7, 1.9, 2.1, 4.2, 6.0} {
		for _, filter := range atempoChain(factor) {
			var v float64
			if _, err := fmtSscan(filter, &v); err != nil {
				t.Fatalf("filtre illisible %q : %v", filter, err)
			}
			if v < 0.5 || v > 2.0 {
				t.Errorf("atempoChain(%v) produit %q, hors des bornes ffmpeg [0.5, 2]", factor, filter)
			}
		}
	}
}

func TestAudioFiltersAreAbsentWithoutSettings(t *testing.T) {
	if got := audioFilters(24000, 1.0, 1.0, nil); got != "" {
		t.Errorf("audioFilters(24000, 1, 1) = %q, want \"\"", got)
	}
}

func TestAudioFiltersOnlyChangeTempoWithoutPitch(t *testing.T) {
	if got := audioFilters(24000, 1.5, 1.0, nil); got != "atempo=1.5" {
		t.Errorf("audioFilters(24000, 1.5, 1) = %q, want \"atempo=1.5\"", got)
	}
}

func TestAudioFiltersRaisePitchWithoutChangingDuration(t *testing.T) {
	// asetrate monte la hauteur ET accélère ; atempo=1/pitch rétablit la durée.
	got := audioFilters(24000, 1.0, 1.25, nil)
	want := "asetrate=30000,aresample=24000,atempo=0.8"
	if got != want {
		t.Errorf("audioFilters(24000, 1, 1.25) = %q, want %q", got, want)
	}
}

func TestAudioFiltersCombinePitchAndSpeed(t *testing.T) {
	// pitch 0.8 (voix plus grave) et vitesse 1.6 → atempo = 1.6 / 0.8 = 2
	got := audioFilters(24000, 1.6, 0.8, nil)
	want := "asetrate=19200,aresample=24000,atempo=2"
	if got != want {
		t.Errorf("audioFilters(24000, 1.6, 0.8, nil) = %q, want %q", got, want)
	}
}

func TestValidSpeedRejectsOutOfBoundsValues(t *testing.T) {
	for _, v := range []float64{0, -1, 0.4, 3.1, 100} {
		if validSpeed(v) {
			t.Errorf("validSpeed(%v) = true, want false", v)
		}
	}
	for _, v := range []float64{0.5, 1, 1.3, 2, 3} {
		if !validSpeed(v) {
			t.Errorf("validSpeed(%v) = false, want true", v)
		}
	}
}

func TestValidPitchRejectsOutOfBoundsValues(t *testing.T) {
	for _, p := range []float64{0, -1, 0.4, 2.1, 50} {
		if validPitch(p) {
			t.Errorf("validPitch(%v) = true, want false", p)
		}
	}
	for _, p := range []float64{0.5, 0.9, 1, 1.5, 2} {
		if !validPitch(p) {
			t.Errorf("validPitch(%v) = false, want true", p)
		}
	}
}

func TestPlayerArgsInsertFiltersOnlyWhenNeeded(t *testing.T) {
	for _, a := range playerArgs(24000, 1.0, 1.0, nil) {
		if a == "-af" {
			t.Fatal("un filtre -af est passé à ffplay alors qu'aucun réglage n'est demandé")
		}
	}

	with := playerArgs(24000, 1.4, 1.0, nil)
	found := false
	for i, a := range with {
		if a == "-af" && i+1 < len(with) && with[i+1] == "atempo=1.4" {
			found = true
		}
	}
	if !found {
		t.Errorf("playerArgs(24000, 1.4, 1) ne contient pas \"-af atempo=1.4\" : %v", with)
	}
}

// fmtSscan relit la valeur numérique d'un filtre "atempo=X".
func fmtSscan(filter string, v *float64) (int, error) {
	var err error
	*v, err = parseFloat(strings.TrimPrefix(filter, "atempo="))
	return 1, err
}
