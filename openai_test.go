package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSynth rend un PCM fixe et retient les énoncés reçus : la route est testée
// sans moteur, donc sans son ni ffmpeg.
type fakeSynth struct {
	mu         sync.Mutex
	enonces    []Utterance
	ecritures  int
	pcm        []byte
	sampleRate int
	err        error
}

func (f *fakeSynth) SynthesizeTo(ctx context.Context, e Utterance, sortie io.Writer) (int, error) {
	f.mu.Lock()
	f.enonces = append(f.enonces, e)
	f.mu.Unlock()
	if f.err != nil {
		return f.sampleRate, f.err
	}
	// Deux écritures : un vrai streaming doit les pousser au fil de l'eau.
	for _, moitie := range [][]byte{f.pcm[:len(f.pcm)/2], f.pcm[len(f.pcm)/2:]} {
		if _, err := sortie.Write(moitie); err != nil {
			return f.sampleRate, err
		}
		f.mu.Lock()
		f.ecritures++
		f.mu.Unlock()
	}
	return f.sampleRate, nil
}

func (f *fakeSynth) SampleRate() int { return f.sampleRate }

func (f *fakeSynth) recu() []Utterance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Utterance{}, f.enonces...)
}

// serveurAvecSynthese monte le routeur complet : file de lecture et synthèse.
func serveurAvecSynthese(t *testing.T, duree time.Duration) (http.Handler, *fakeSpeaker, *Controller, *fakeSynth) {
	t.Helper()
	sp := newFakeSpeaker(duree)
	c := NewController(sp)
	c.Start()
	t.Cleanup(c.Close)
	synth := &fakeSynth{pcm: []byte{1, 2, 3, 4, 5, 6}, sampleRate: 24000}
	h := newServer(c, synth, "estelle", func() []string { return []string{"estelle", "jean", "alba"} }, 1.0, 1.0)
	return h, sp, c, synth
}

func appelJSON(t *testing.T, h http.Handler, cible, corps string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, cible, strings.NewReader(corps))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSpeechRenvoieUnWavCompletParDefaut(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type = %q, want \"audio/wav\"", ct)
	}
	corps := rec.Body.Bytes()
	if len(corps) != wavHeaderSize+len(synth.pcm) {
		t.Fatalf("taille du corps = %d, want %d", len(corps), wavHeaderSize+len(synth.pcm))
	}
	if !bytes.Equal(corps[:4], []byte("RIFF")) || !bytes.Equal(corps[8:12], []byte("WAVE")) {
		t.Errorf("le corps ne commence pas par un en-tête RIFF/WAVE : % x", corps[:12])
	}
	if !bytes.Equal(corps[wavHeaderSize:], synth.pcm) {
		t.Errorf("données audio = % x, want % x", corps[wavHeaderSize:], synth.pcm)
	}
	if taux := binary.LittleEndian.Uint32(corps[24:]); taux != 24000 {
		t.Errorf("sample rate dans l'en-tête = %d, want 24000", taux)
	}
	if got := rec.Header().Get("X-Sample-Rate"); got != "24000" {
		t.Errorf("X-Sample-Rate = %q, want \"24000\"", got)
	}
}

func TestSpeechNePasseParLaFileDeLecture(t *testing.T) {
	h, sp, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour"}`)

	rec := appel(t, h, http.MethodGet, "/queue", "")
	var etat State
	if err := json.Unmarshal(rec.Body.Bytes(), &etat); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if etat.Current.Text != "" || len(etat.Pending) != 0 {
		t.Errorf("file = %+v, want vide : la synthèse ne doit rien jouer localement", etat)
	}
	select {
	case texte := <-sp.demarre:
		t.Errorf("le moteur de lecture a prononcé %q alors qu'on demandait de l'audio", texte)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSpeechAppliqueLesDefautsDuService(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour"}`)

	recu := synth.recu()
	if len(recu) != 1 {
		t.Fatalf("énoncés synthétisés = %d, want 1", len(recu))
	}
	if recu[0].Voice != "estelle" || recu[0].Speed != 1.0 || recu[0].Pitch != 1.0 {
		t.Errorf("énoncé = %+v, want estelle/1/1", recu[0])
	}
}

func TestSpeechTransmetVoixVitesseEtEffets(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech",
		`{"input":"combo","voice":"jean","speed":1.33,"pitch":1.1,"effects":[{"name":"echo","force":0.25}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusOK, rec.Body)
	}

	recu := synth.recu()
	if len(recu) != 1 {
		t.Fatalf("énoncés synthétisés = %d, want 1", len(recu))
	}
	e := recu[0]
	if e.Text != "combo" || e.Voice != "jean" || e.Speed != 1.33 || e.Pitch != 1.1 {
		t.Errorf("énoncé = %+v, want combo/jean/1.33/1.1", e)
	}
	if len(e.Effects) != 1 || e.Effects[0].Name != "echo" || e.Effects[0].Force != 0.25 {
		t.Errorf("effets = %+v, want [echo/0.25]", e.Effects)
	}
}

func TestSpeechRetombeSurLaVoixParDefautSiElleEstInconnue(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	// ff_siwis est un nom de voix Kokoro : un client OpenAI l'envoie sans savoir
	// quel moteur est derrière, la requête doit aboutir quand même.
	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour","voice":"ff_siwis"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if got := rec.Header().Get("X-Voice-Used"); got != "estelle" {
		t.Errorf("X-Voice-Used = %q, want \"estelle\"", got)
	}
	if recu := synth.recu(); len(recu) != 1 || recu[0].Voice != "estelle" {
		t.Errorf("voix synthétisée = %+v, want estelle", recu)
	}
}

func TestSpeechIgnoreLesChampsPropresAOpenAI(t *testing.T) {
	h, _, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech",
		`{"model":"kokoro","input":"bonjour","lang_code":"f","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusOK, rec.Body)
	}
}

func TestSpeechRenvoieDuPcmBrutSurDemande(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour","response_format":"pcm"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if !bytes.Equal(rec.Body.Bytes(), synth.pcm) {
		t.Errorf("corps = % x, want le PCM nu % x", rec.Body.Bytes(), synth.pcm)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "audio/pcm") {
		t.Errorf("Content-Type = %q, want un audio/pcm", ct)
	}
}

func TestSpeechRefuseUnFormatNonServi(t *testing.T) {
	h, _, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour","response_format":"mp3"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d pour un format non servi", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "wav") {
		t.Errorf("le message devrait citer les formats servis, got %s", rec.Body)
	}
}

func TestSpeechRefuseUnTexteVide(t *testing.T) {
	h, _, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour un texte vide", rec.Code, http.StatusBadRequest)
	}
}

func TestSpeechRefuseUnJSONInvalide(t *testing.T) {
	h, _, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour un JSON invalide", rec.Code, http.StatusBadRequest)
	}
}

func TestSpeechRefuseUneVitesseHorsBornes(t *testing.T) {
	h, _, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"trop vite","speed":9}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour une vitesse hors bornes", rec.Code, http.StatusBadRequest)
	}
}

func TestSpeechRefuseUnEffetInconnu(t *testing.T) {
	h, _, _, _ := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"x","effects":[{"name":"dubstep"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d pour un effet inconnu", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "dubstep") {
		t.Errorf("le message devrait citer l'effet refusé, got %s", rec.Body)
	}
}

func TestSpeechSignaleUnEchecDeSynthese(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)
	synth.err = errEmptyText // n'importe quelle erreur du moteur

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want %d quand le moteur échoue", rec.Code, http.StatusInternalServerError)
	}
}

func TestSpeechRepond503SansMoteurDeSynthese(t *testing.T) {
	sp := newFakeSpeaker(50 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	t.Cleanup(c.Close)
	h := newServer(c, nil, "estelle", func() []string { return nil }, 1.0, 1.0)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want %d sans synthétiseur", rec.Code, http.StatusServiceUnavailable)
	}
	// Les routes de lecture, elles, restent servies.
	if rec := appel(t, h, http.MethodPost, "/say", "bonjour"); rec.Code != http.StatusAccepted {
		t.Errorf("/say code = %d, want %d", rec.Code, http.StatusAccepted)
	}
}

func TestSpeechStreamePcmAuFilDeLEau(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour","stream":true,"response_format":"pcm"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusOK, rec.Body)
	}
	if !bytes.Equal(rec.Body.Bytes(), synth.pcm) {
		t.Errorf("corps = % x, want le PCM nu % x", rec.Body.Bytes(), synth.pcm)
	}
	// Pas de Content-Length : la longueur est inconnue quand les en-têtes partent.
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want vide en streaming", got)
	}
	if got := rec.Header().Get("X-Sample-Rate"); got != "24000" {
		t.Errorf("X-Sample-Rate = %q, want \"24000\"", got)
	}
	if synth.ecritures != 2 {
		t.Errorf("écritures poussées = %d, want 2 (flush par morceau)", synth.ecritures)
	}
}

func TestSpeechStreameWavAvecUnEnteteDeLongueurInconnue(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	corps := rec.Body.Bytes()
	if len(corps) != wavHeaderSize+len(synth.pcm) {
		t.Fatalf("taille = %d, want %d", len(corps), wavHeaderSize+len(synth.pcm))
	}
	if taille := binary.LittleEndian.Uint32(corps[40:]); taille != unknownSize {
		t.Errorf("taille du bloc data = %#x, want %#x en streaming", taille, uint32(unknownSize))
	}
	if !bytes.Equal(corps[wavHeaderSize:], synth.pcm) {
		t.Error("l'audio ne suit pas l'en-tête")
	}
}

func TestSpeechSansStreamRenvoieToujoursDesTaillesReelles(t *testing.T) {
	h, _, _, synth := serveurAvecSynthese(t, 50*time.Millisecond)

	rec := appelJSON(t, h, "/v1/audio/speech", `{"input":"bonjour","stream":false}`)
	corps := rec.Body.Bytes()
	if taille := binary.LittleEndian.Uint32(corps[40:]); taille != uint32(len(synth.pcm)) {
		t.Errorf("taille du bloc data = %d, want %d hors streaming", taille, len(synth.pcm))
	}
	if got := rec.Header().Get("Content-Length"); got == "" {
		t.Error("Content-Length absent hors streaming")
	}
}
