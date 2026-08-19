package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// maxTextSize borne le corps accepté par /say et /v1/audio/speech
// (~50 000 caractères, largement au-delà de ce qu'on veut écouter d'une traite).
const maxTextSize = 50 << 10

// settings porte les valeurs par défaut du service et le catalogue de voix.
// Partagé par /say et /v1/audio/speech, pour que les deux routes valident
// exactement les mêmes bornes.
type settings struct {
	defaultVoice string
	// knownVoices est un appel, pas une liste figée : le moteur paresseux ne
	// connaît son catalogue qu'après son premier réveil. Vide tant qu'il n'a
	// pas démarré, ce qui désactive la validation — comme avant son premier
	// démarrage, aucune voix n'est encore « connue ».
	knownVoices  func() []string
	defaultSpeed float64
	defaultPitch float64
}

// normalize nettoie le texte de l'énoncé, le complète avec les défauts du
// service, puis le valide.
//
// freeVoice remplace une voix inconnue par la voix par défaut au lieu de
// refuser la requête : les clients compatibles OpenAI envoient les noms de voix
// de leur fournisseur habituel, et les refuser les casserait tous. Sur /say,
// où la voix est choisie explicitement, une faute de frappe doit se voir : la
// route y passe false.
func (s settings) normalize(u Utterance, freeVoice bool) (Utterance, error) {
	// Le tokenizer du modèle ne connaît qu'une partie des caractères du
	// français : on ramène le texte dans son vocabulaire ici, au point de
	// passage commun aux deux routes, pour que /say et /v1/audio/speech
	// synthétisent exactement le même texte.
	u.Text = cleanText(u.Text)
	if u.Text == "" {
		return u, errUnspeakableText
	}

	if u.Voice == "" {
		u.Voice = s.defaultVoice
	}
	if u.Speed == 0 {
		u.Speed = s.defaultSpeed
	}
	if u.Pitch == 0 {
		u.Pitch = s.defaultPitch
	}

	if !validSpeed(u.Speed) {
		return u, textError(fmt.Sprintf(
			"speed %v out of bounds: expected between %v and %v", u.Speed, speedMin, speedMax))
	}
	if !validPitch(u.Pitch) {
		return u, textError(fmt.Sprintf(
			"pitch %v out of bounds: expected between %v and %v", u.Pitch, pitchMin, pitchMax))
	}
	if knownVoices := s.knownVoices(); len(knownVoices) > 0 && !slices.Contains(knownVoices, u.Voice) {
		if !freeVoice {
			return u, textError(fmt.Sprintf(
				"unknown voice %q — available voices: %s", u.Voice, strings.Join(knownVoices, ", ")))
		}
		u.Voice = s.defaultVoice
	}

	for _, effect := range u.Effects {
		if _, known := effectFilter(effect.Name, 1.0); !known {
			return u, textError(fmt.Sprintf(
				"unknown effect %q — available effects: %s",
				effect.Name, strings.Join(availableEffects(), ", ")))
		}
		if !validForce(effect.Force) {
			return u, textError(fmt.Sprintf(
				"force %v out of bounds for effect %q: expected between %v and %v",
				effect.Force, effect.Name, forceMin, forceMax))
		}
	}
	return u, nil
}

// newServer monte les routes. knownVoices sert à refuser tout de suite une voix
// invalide plutôt que de laisser le moteur échouer une fois l'énoncé en file ;
// une liste vide désactive la validation — notamment tant que le moteur
// paresseux n'a pas encore démarré. synth peut être nil : /v1/audio/speech
// répond alors 503, les routes de lecture restent servies.
func newServer(c *Controller, synth Synthesizer, defaultVoice string, knownVoices func() []string, defaultSpeed, defaultPitch float64) http.Handler {
	mux := http.NewServeMux()
	s := settings{
		defaultVoice: defaultVoice,
		knownVoices:  knownVoices,
		defaultSpeed: defaultSpeed,
		defaultPitch: defaultPitch,
	}

	handleSay := func(w http.ResponseWriter, r *http.Request) {
		text, voice, speed, pitch, effects, err := extractUtterance(r)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}

		utterance, err := s.normalize(
			Utterance{Text: text, Voice: voice, Speed: speed, Pitch: pitch, Effects: effects}, false)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}

		respondJSON(w, http.StatusAccepted, map[string]any{
			"position": c.Enqueue(utterance),
			"text":     utterance.Text,
			"voice":    utterance.Voice,
			"speed":    utterance.Speed,
			"pitch":    utterance.Pitch,
			"effects":  utterance.Effects,
		})
	}
	mux.HandleFunc("/say", handleSay)
	mux.HandleFunc("/gladyss", handleSay)

	// Synthèse compatible OpenAI : rend l'audio au client au lieu de le jouer.
	mux.HandleFunc("/v1/audio/speech", speech(synth, s))

	mux.HandleFunc("/skip", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{"interrupted": c.Skip()})
	})

	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{"removed": c.Stop()})
	})

	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, c.Snapshot())
	})

	mux.HandleFunc("/voices", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{
			"default": defaultVoice,
			"voices":  knownVoices(),
			"speed":   defaultSpeed,
			"pitch":   defaultPitch,
		})
	})

	mux.HandleFunc("/effects", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{
			"effects":   availableEffects(),
			"force_min": forceMin,
			"force_max": forceMax,
		})
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	return mux
}

// extractUtterance accepte le texte et la voix sous trois formes, de la plus
// pratique en curl à la plus structurée : paramètres de requête, corps brut,
// ou corps JSON {"text":"…","voice":"…"}.
func extractUtterance(r *http.Request) (text, voice string, speed, pitch float64, effects []Effect, err error) {
	voice = strings.TrimSpace(r.URL.Query().Get("voice"))

	if raw := strings.TrimSpace(r.URL.Query().Get("speed")); raw != "" {
		speed, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", voice, 0, 0, nil, textError(fmt.Sprintf("unreadable speed: %q", raw))
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("pitch")); raw != "" {
		pitch, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", voice, speed, 0, nil, textError(fmt.Sprintf("unreadable pitch: %q", raw))
		}
	}

	// ?fx=robot,vibrato — raccourci en ligne de commande, force nominale.
	if raw := strings.TrimSpace(r.URL.Query().Get("fx")); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				effects = append(effects, Effect{Name: name})
			}
		}
	}

	if raw := r.URL.Query().Get("text"); strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw), voice, speed, pitch, effects, nil
	}

	body, readErr := io.ReadAll(io.LimitReader(r.Body, maxTextSize))
	if readErr != nil {
		return "", voice, speed, pitch, effects, errUnreadableText
	}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var payload struct {
			Text    string   `json:"text"`
			Voice   string   `json:"voice"`
			Speed   float64  `json:"speed"`
			Pitch   float64  `json:"pitch"`
			Effects []Effect `json:"effects"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return "", voice, speed, pitch, effects, errInvalidJSON
		}
		if v := strings.TrimSpace(payload.Voice); v != "" {
			voice = v // le corps l'emporte sur le paramètre de requête
		}
		if payload.Speed != 0 {
			speed = payload.Speed
		}
		if payload.Pitch != 0 {
			pitch = payload.Pitch
		}
		if len(payload.Effects) > 0 {
			effects = payload.Effects // le corps l'emporte sur ?fx=
		}
		if text := strings.TrimSpace(payload.Text); text != "" {
			return text, voice, speed, pitch, effects, nil
		}
		return "", voice, speed, pitch, effects, errEmptyText
	}

	if text := strings.TrimSpace(string(body)); text != "" {
		return text, voice, speed, pitch, effects, nil
	}
	return "", voice, speed, pitch, effects, errEmptyText
}

type textError string

func (e textError) Error() string { return string(e) }

const (
	errEmptyText      = textError("no text provided: use ?text=…, a raw body, or {\"text\":\"…\"}")
	errUnreadableText = textError("unreadable request body")
	errInvalidJSON    = textError("invalid JSON body")
	// Cas limite d'un texte fait des seuls caractères muets (apostrophes,
	// guillemets, puces) : il n'en reste rien à prononcer, et l'énoncé vide
	// traverserait la file jusqu'au daemon.
	errUnspeakableText = textError("empty text: nothing left to speak after cleanup")
)

func respondJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

func respondError(w http.ResponseWriter, code int, message string) {
	respondJSON(w, code, map[string]string{"error": message})
}
