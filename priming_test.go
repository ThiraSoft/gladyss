package main

import (
	"bytes"
	"testing"
)

func TestPrimingWriterRemplitDeSilenceTantQueRienNArrive(t *testing.T) {
	var joue bytes.Buffer
	w := newPrimingWriter(&joue, 24000, 1.0)

	for i := 0; i < 5; i++ {
		if err := w.tick(); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}

	attendu := 5 * silenceSize(primingTick, 24000, 1.0)
	if joue.Len() != attendu {
		t.Fatalf("silence écrit = %d octets, attendu %d", joue.Len(), attendu)
	}
	if !estSilence(joue.Bytes()) {
		t.Fatal("l'amorçage doit être du silence")
	}
}

func TestPrimingWriterSeCoupeAuPremierOctetDeParole(t *testing.T) {
	var joue bytes.Buffer
	w := newPrimingWriter(&joue, 24000, 1.0)

	if err := w.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	amorce := joue.Len()

	parole := bytes.Repeat([]byte{0x11, 0x22}, 100)
	if _, err := w.Write(parole); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Après la parole, les ticks en vol ne doivent plus rien écrire.
	for i := 0; i < 3; i++ {
		if err := w.tick(); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}

	coussin := silenceSize(primingCushion, 24000, 1.0)
	if got, want := joue.Len(), amorce+coussin+len(parole); got != want {
		t.Fatalf("total écrit = %d octets, attendu %d", got, want)
	}
	// La parole est intacte et rien ne s'est intercalé dedans.
	if !bytes.Equal(joue.Bytes()[amorce+coussin:], parole) {
		t.Fatal("la parole a été altérée ou entrecoupée de silence")
	}
	if !estSilence(joue.Bytes()[:amorce+coussin]) {
		t.Fatal("tout ce qui précède la parole doit être du silence")
	}
}

func TestPrimingWriterSuitLaVitesseDeLecture(t *testing.T) {
	var joue bytes.Buffer
	w := newPrimingWriter(&joue, 24000, 2.0)

	if err := w.tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Le lecteur avale deux secondes d'audio par seconde : il faut lui en
	// fournir deux fois plus pour tenir la même durée d'horloge.
	if got, want := joue.Len(), 2*silenceSize(primingTick, 24000, 1.0); got != want {
		t.Fatalf("silence écrit = %d octets, attendu %d", got, want)
	}
}

func estSilence(b []byte) bool {
	for _, o := range b {
		if o != 0 {
			return false
		}
	}
	return true
}
