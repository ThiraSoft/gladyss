package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveurDeTest monte le routeur au-dessus d'un Controller piloté par un faux
// Speaker. Le synthétiseur est monté aussi mais ne sert pas ici : voir
// serveurAvecSynthese pour les tests de /v1/audio/speech.
func serveurDeTest(t *testing.T, duree time.Duration) (http.Handler, *fakeSpeaker, *Controller) {
	t.Helper()
	h, sp, c, _ := serveurAvecSynthese(t, duree)
	return h, sp, c
}

func appel(t *testing.T, h http.Handler, methode, cible, corps string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(methode, cible, strings.NewReader(corps))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSayMetLeTexteEnFileEtRenvoieSaPosition(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 300*time.Millisecond)

	rec := appel(t, h, http.MethodPost, "/say", "bonjour")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}

	var reponse struct {
		Position int    `json:"position"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reponse); err != nil {
		t.Fatalf("réponse illisible: %v — corps: %s", err, rec.Body)
	}
	if reponse.Position != 1 {
		t.Errorf("position = %d, want 1 pour le premier énoncé", reponse.Position)
	}
	if reponse.Text != "bonjour" {
		t.Errorf("text = %q, want \"bonjour\"", reponse.Text)
	}
	if got := sp.attendDemarrage(t); got != "bonjour" {
		t.Errorf("énoncé lu = %q, want \"bonjour\"", got)
	}
}

func TestSayAccepteLeTexteEnParametreDeRequete(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=salut+toi", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if got := sp.attendDemarrage(t); got != "salut toi" {
		t.Errorf("énoncé lu = %q, want \"salut toi\"", got)
	}
}

func TestSayAccepteUnCorpsJSON(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/say", strings.NewReader(`{"text":"en json"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	if got := sp.attendDemarrage(t); got != "en json" {
		t.Errorf("énoncé lu = %q, want \"en json\"", got)
	}
}

func TestSayRefuseUnTexteVide(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodPost, "/say", "   ")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour un texte vide", rec.Code, http.StatusBadRequest)
	}
}

func TestSkipRenvoieSiUnEnonceAEteInterrompu(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 300*time.Millisecond)

	appel(t, h, http.MethodPost, "/say", "un")
	sp.attendDemarrage(t)

	rec := appel(t, h, http.MethodPost, "/skip", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	var reponse struct {
		Interrupted bool `json:"interrupted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reponse); err != nil {
		t.Fatalf("réponse illisible: %v — corps: %s", err, rec.Body)
	}
	if !reponse.Interrupted {
		t.Error("interrupted = false alors qu'un énoncé était en cours")
	}
}

func TestStopRenvoieLeNombreDEnoncesRetires(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 300*time.Millisecond)

	appel(t, h, http.MethodPost, "/say", "un")
	appel(t, h, http.MethodPost, "/say", "deux")
	appel(t, h, http.MethodPost, "/say", "trois")
	sp.attendDemarrage(t)

	rec := appel(t, h, http.MethodPost, "/stop", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	var reponse struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reponse); err != nil {
		t.Fatalf("réponse illisible: %v — corps: %s", err, rec.Body)
	}
	if reponse.Removed != 2 {
		t.Errorf("removed = %d, want 2", reponse.Removed)
	}
}

func TestQueueExposeLEtatDeLaFile(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 300*time.Millisecond)

	appel(t, h, http.MethodPost, "/say", "un")
	appel(t, h, http.MethodPost, "/say", "deux")
	sp.attendDemarrage(t)

	rec := appel(t, h, http.MethodGet, "/queue", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	var etat State
	if err := json.Unmarshal(rec.Body.Bytes(), &etat); err != nil {
		t.Fatalf("réponse illisible: %v — corps: %s", err, rec.Body)
	}
	if etat.Current.Text != "un" {
		t.Errorf("current = %q, want \"un\"", etat.Current.Text)
	}
	if len(etat.Pending) != 1 || etat.Pending[0].Text != "deux" {
		t.Errorf("pending = %v, want [deux]", etat.Pending)
	}
}

func TestSayUtiliseLaVoixDemandee(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=bonjour&voice=jean", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)

	voix := sp.voixDemandees()
	if len(voix) != 1 || voix[0] != "jean" {
		t.Errorf("voix transmise au moteur = %v, want [jean]", voix)
	}
}

func TestSaySansVoixUtiliseLaVoixParDefaut(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	appel(t, h, http.MethodPost, "/say", "bonjour")
	sp.attendDemarrage(t)

	voix := sp.voixDemandees()
	if len(voix) != 1 || voix[0] != "estelle" {
		t.Errorf("voix transmise au moteur = %v, want [estelle]", voix)
	}
}

func TestSayAccepteLaVoixDansLeCorpsJSON(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/say", strings.NewReader(`{"text":"bonjour","voice":"alba"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)
	if voix := sp.voixDemandees(); len(voix) != 1 || voix[0] != "alba" {
		t.Errorf("voix transmise au moteur = %v, want [alba]", voix)
	}
}

func TestSayRefuseUneVoixInconnue(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=bonjour&voice=jean-claude", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d pour une voix inconnue", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "jean-claude") {
		t.Errorf("le message d'erreur devrait citer la voix refusée, got %s", rec.Body)
	}
}

func TestVoicesListeLesVoixDisponibles(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/voices", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	var reponse struct {
		Default string   `json:"default"`
		Voices  []string `json:"voices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reponse); err != nil {
		t.Fatalf("réponse illisible: %v — corps: %s", err, rec.Body)
	}
	if reponse.Default != "estelle" {
		t.Errorf("default = %q, want \"estelle\"", reponse.Default)
	}
	if len(reponse.Voices) != 3 {
		t.Errorf("voices = %v, want 3 entrées", reponse.Voices)
	}
}

func TestSayTransmetLaVitesseDemandee(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=vite&speed=1.6", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)

	if v := sp.vitessesDemandees(); len(v) != 1 || v[0] != 1.6 {
		t.Errorf("vitesse transmise au moteur = %v, want [1.6]", v)
	}
}

func TestSaySansVitesseUtiliseCelleDuService(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	appel(t, h, http.MethodPost, "/say", "normal")
	sp.attendDemarrage(t)

	if v := sp.vitessesDemandees(); len(v) != 1 || v[0] != 1.0 {
		t.Errorf("vitesse transmise au moteur = %v, want [1]", v)
	}
}

func TestSayRefuseUneVitesseHorsBornes(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=trop+vite&speed=9", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour une vitesse hors bornes", rec.Code, http.StatusBadRequest)
	}
}

func TestSayRefuseUneVitesseIllisible(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=bonjour&speed=rapide", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour une vitesse illisible", rec.Code, http.StatusBadRequest)
	}
}

func TestSayTransmetLaHauteurDemandee(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=grave&pitch=0.8", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)

	if p := sp.pitchsDemandes(); len(p) != 1 || p[0] != 0.8 {
		t.Errorf("hauteur transmise au moteur = %v, want [0.8]", p)
	}
}

func TestSayRefuseUneHauteurHorsBornes(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=aigu&pitch=5", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour une hauteur hors bornes", rec.Code, http.StatusBadRequest)
	}
}

func TestSayAccepteHauteurEtVitesseEnsemble(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/say",
		strings.NewReader(`{"text":"combo","speed":1.4,"pitch":1.2}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)

	if v := sp.vitessesDemandees(); len(v) != 1 || v[0] != 1.4 {
		t.Errorf("vitesse = %v, want [1.4]", v)
	}
	if p := sp.pitchsDemandes(); len(p) != 1 || p[0] != 1.2 {
		t.Errorf("hauteur = %v, want [1.2]", p)
	}
}

func TestSayTransmetLesEffetsDemandes(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/say",
		strings.NewReader(`{"text":"bonjour","effects":[{"name":"echo","force":1.5}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)

	effets := sp.effetsDemandes()
	if len(effets) != 1 || len(effets[0]) != 1 {
		t.Fatalf("effets transmis = %v, want un effet", effets)
	}
	if effets[0][0].Name != "echo" || effets[0][0].Force != 1.5 {
		t.Errorf("effet = %+v, want echo/1.5", effets[0][0])
	}
}

func TestSayAccepteLeRaccourciFx(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=vite&fx=echo", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	sp.attendDemarrage(t)

	effets := sp.effetsDemandes()
	if len(effets) != 1 || len(effets[0]) != 1 {
		t.Fatalf("effets transmis = %v, want un effet", effets)
	}
	if effets[0][0].Name != "echo" {
		t.Errorf("effets = %+v, want [echo]", effets[0])
	}
}

func TestSayRefuseUnEffetInconnu(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/say?text=x&fx=dubstep", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "dubstep") {
		t.Errorf("le message devrait citer l'effet refusé, got %s", rec.Body)
	}
}

func TestSayRefuseUneForceHorsBornes(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodPost, "/say",
		strings.NewReader(`{"text":"x","effects":[{"name":"echo","force":9}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want %d pour une force hors bornes", rec.Code, http.StatusBadRequest)
	}
}

func TestEffectsListeLesEffetsDisponibles(t *testing.T) {
	h, _, _ := serveurDeTest(t, 50*time.Millisecond)

	rec := appel(t, h, http.MethodGet, "/effects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusOK)
	}
	var reponse struct {
		Effects []string `json:"effects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reponse); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if len(reponse.Effects) != 1 || reponse.Effects[0] != "echo" {
		t.Errorf("effects = %v, want [echo]", reponse.Effects)
	}
}
