package main

import (
	"bytes"
	"testing"
	"time"
)

// horlogeFactice est avancée par le test à chaque morceau écrit : le débit de
// génération devient une donnée du test, pas une propriété de la machine.
type horlogeFactice struct{ instant time.Time }

func (h *horlogeFactice) maintenant() time.Time { return h.instant }

// pcm rend n millisecondes d'audio à 24 kHz, 16 bits mono.
func pcm(ms int) []byte {
	return make([]byte, ms*24*bytesPerSample)
}

// pacerFactice monte un pacedWriter et l'écrivain qui l'alimente par morceaux
// de 100 ms d'audio, en faisant avancer l'horloge de `pas` à chaque morceau :
// le débit de génération vaut 100 ms / pas.
func pacerFactice(out *bytes.Buffer, vitesse float64, texte string, pas time.Duration) (*pacedWriter, func(ms int)) {
	w := newPacedWriter(out, 24000, vitesse, texte, 0)
	h := &horlogeFactice{instant: time.Unix(0, 0)}
	w.now = h.maintenant
	return w, func(ms int) {
		h.instant = h.instant.Add(time.Duration(ms) * pas / 100)
		_, _ = w.Write(pcm(ms))
	}
}

func TestPacedWriterJoueAuFilDeLEauQuandLaGenerationSuit(t *testing.T) {
	var joue bytes.Buffer
	// 100 ms d'audio produites en 50 ms : rate = 2×, bien au-dessus de 1,1×.
	w, ecrire := pacerFactice(&joue, 1.1, "un texte de longueur quelconque", 50*time.Millisecond)

	for i := 0; i < 6; i++ {
		ecrire(100)
	}

	// Le premier morceau amorce la mesure, le suivant l'achève : la lecture doit
	// avoir démarré bien avant le sixième.
	if joue.Len() == 0 {
		t.Fatal("rien n'a été joué : le lecteur attend alors que la génération suit")
	}
	if got := w.duration(joue.Len()); got < 500*time.Millisecond {
		t.Errorf("lecture partielle : %v joués sur 600 ms produites", got)
	}
}

func TestPacedWriterConstitueUneAvanceQuandLaGenerationNeSuitPas(t *testing.T) {
	var joue bytes.Buffer
	// 100 ms d'audio produites en 100 ms : rate = 1×, pour une lecture à 2×.
	// L'énoncé est estimé à 2 s (20 caractères) : avance attendue = 2 s × 0,5 = 1 s.
	w, ecrire := pacerFactice(&joue, 2.0, "vingt caracteres....", 100*time.Millisecond)

	for i := 0; i < 5; i++ { // 500 ms d'audio : sous l'avance visée
		ecrire(100)
	}
	if joue.Len() != 0 {
		t.Fatalf("lecture démarrée avec %v d'avance, moins que le seuil", w.duration(joue.Len()))
	}

	for i := 0; i < 6; i++ { // au total 1,1 s : l'avance est constituée
		ecrire(100)
	}
	if joue.Len() == 0 {
		t.Fatal("l'avance est atteinte mais la lecture n'a pas démarré")
	}
}

func TestPacedWriterBorneLAvanceExigee(t *testing.T) {
	var joue bytes.Buffer
	texte := string(make([]rune, 2000)) // 200 s estimées : l'avance déborderait
	w, ecrire := pacerFactice(&joue, 3.0, texte, 200*time.Millisecond)

	ecrire(100)
	ecrire(500)
	if w.lead > maxLead {
		t.Errorf("avance exigée %v au-delà de la borne %v", w.lead, maxLead)
	}
}

func TestPacedWriterFlushJoueLesEnoncesTropCourts(t *testing.T) {
	var joue bytes.Buffer
	w, ecrire := pacerFactice(&joue, 2.0, "texte", 100*time.Millisecond)

	ecrire(50)
	if joue.Len() != 0 {
		t.Fatal("50 ms ne suffisent pas à décider : rien ne doit partir")
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush : %v", err)
	}
	if w.duration(joue.Len()) != 50*time.Millisecond {
		t.Errorf("flush a joué %v au lieu des 50 ms retenues", w.duration(joue.Len()))
	}
}

func TestPacedWriterSePasseDeMesureQuandLeDebitPrecedentSuffit(t *testing.T) {
	var joue bytes.Buffer
	// Débit hérité de 2× pour une lecture à 1,1× : le premier morceau part sans
	// attendre la moindre mesure.
	w := newPacedWriter(&joue, 24000, 1.1, "un texte", 2.0)

	if _, err := w.Write(pcm(30)); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	if w.duration(joue.Len()) != 30*time.Millisecond {
		t.Errorf("le premier morceau a été retenu : %v joués", w.duration(joue.Len()))
	}
}

func TestPacedWriterIgnoreUnDebitPrecedentInsuffisant(t *testing.T) {
	var joue bytes.Buffer
	w := newPacedWriter(&joue, 24000, 2.0, "un texte", 1.0) // 1× hérité, lecture à 2×

	_, _ = w.Write(pcm(30))
	if joue.Len() != 0 {
		t.Error("le débit hérité ne couvre pas la vitesse : la mesure reste obligatoire")
	}
}

func TestEstimateDurationSuitLaLongueur(t *testing.T) {
	if got := estimateDuration("dix caract"); got != time.Second {
		t.Errorf("10 caractères estimés à %v au lieu de 1 s", got)
	}
	if estimateDuration("") != 0 {
		t.Error("un texte vide ne dure pas")
	}
}
