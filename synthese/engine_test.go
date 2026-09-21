package synthese

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThiraSoft/golem/pockettts"
)

// moteurDeTest ouvre le vrai moteur sur les poids du cache Hugging Face, avec
// la voix du dépôt. Il saute si le modèle n'est pas là : un clone nu doit
// pouvoir lancer go test.
func moteurDeTest(t *testing.T) *Moteur {
	t.Helper()
	lang, err := pockettts.LookupLanguage("french_24l")
	if err != nil {
		t.Fatal(err)
	}
	if pockettts.Locate(lang.WeightsPath()) == "" || pockettts.Locate(lang.TokenizerPath()) == "" {
		t.Skip("poids Pocket TTS absents du cache Hugging Face")
	}
	if _, err := os.Stat(filepath.Join("voix", "gladyss.safetensors")); err != nil {
		t.Skip("voix/gladyss.safetensors absent")
	}
	p, err := Ouvrir("voix", "gladyss", "ffplay", "ffmpeg", 1.0, 1.0, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestSampleRateAvantToutEnonce(t *testing.T) {
	p := moteurDeTest(t)
	if p.SampleRate() != 24000 {
		t.Errorf("SampleRate = %d, attendu 24000", p.SampleRate())
	}
}

func TestSynthesizeRendDuPCM(t *testing.T) {
	p := moteurDeTest(t)
	audio, rate, err := p.Synthesize(context.Background(), Enonce{Text: "Bonjour."})
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24000 {
		t.Errorf("rate = %d, attendu 24000", rate)
	}
	if len(audio) < 2 {
		t.Fatalf("audio de %d octets, attendu du son", len(audio))
	}
	if len(audio)%2 != 0 {
		t.Errorf("audio de %d octets, attendu un nombre pair (s16le)", len(audio))
	}
	if bytes.Equal(audio, make([]byte, len(audio))) {
		t.Error("audio entièrement silencieux")
	}
}

// Un contexte annulé arrête la génération, et ce n'est pas une erreur du point
// de vue du service : c'est ce que fait --stop.
func TestSynthesizeAnnule(t *testing.T) {
	p := moteurDeTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := p.Synthesize(ctx, Enonce{Text: "Une phrase que personne n'entendra jamais."})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, attendu context.Canceled", err)
	}
}

// Une voix inconnue est rejetée avec son nom, pas avec « aucun audio produit ».
func TestSynthesizeVoixInconnue(t *testing.T) {
	p := moteurDeTest(t)
	_, _, err := p.Synthesize(context.Background(), Enonce{Text: "Bonjour.", Voice: "personne"})
	if err == nil {
		t.Fatal("attendu une erreur pour une voix inconnue")
	}
	if !strings.Contains(err.Error(), "personne") {
		t.Errorf("erreur = %q, attendu qu'elle nomme la voix", err)
	}
}

func TestVoicesContientLaVoixLocale(t *testing.T) {
	p := moteurDeTest(t)
	for _, n := range p.Voices() {
		if n == "gladyss" {
			return
		}
	}
	t.Errorf("Voices = %v, attendu qu'il contienne gladyss", p.Voices())
}

// TestFramesEtSynthesizeToDisentLaMemeChose vérifie que les deux sorties du
// moteur ne divergent pas : SynthesizeTo doit rendre exactement le PCM des
// frames que Frames a données, sinon avatar et nova n'entendent pas la même
// chose.
func TestFramesEtSynthesizeToDisentLaMemeChose(t *testing.T) {
	if testing.Short() {
		t.Skip("charge le modèle")
	}
	m := moteurDeTest(t)
	e := Enonce{Text: "Le vent se lève, il faut tenter de vivre."}

	var frames []float32
	if err := m.Frames(context.Background(), e, func(f []float32) {
		frames = append(frames, f...)
	}); err != nil {
		t.Fatalf("Frames: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("Frames n'a rendu aucun échantillon")
	}

	var buf bytes.Buffer
	if _, err := m.SynthesizeTo(context.Background(), e, &buf); err != nil {
		t.Fatalf("SynthesizeTo: %v", err)
	}
	if got, want := buf.Len(), 2*len(frames); got != want {
		t.Errorf("SynthesizeTo a rendu %d octets, les frames en valent %d", got, want)
	}
}

// TestPCMEcrete vérifie que les valeurs hors bornes sont écrêtées plutôt que
// de déborder : un débordement s'entendrait bien plus qu'un écrêtage.
func TestPCMEcrete(t *testing.T) {
	out := PCM([]float32{0, 1, -1, 2, -2})
	want := []int16{0, 32767, -32768, 32767, -32768}
	for i, w := range want {
		got := int16(binary.LittleEndian.Uint16(out[2*i:]))
		if got != w {
			t.Errorf("échantillon %d = %d, attendu %d", i, got, w)
		}
	}
}
