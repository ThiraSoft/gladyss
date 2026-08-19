package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// Synthesizer rend un énoncé en audio au lieu de le jouer sur les
// haut-parleurs de la machine.
type Synthesizer interface {
	// SynthesizeTo écrit le PCM (signé 16 bits, little-endian, mono) dans
	// sortie au fil de la génération, et renvoie son taux d'échantillonnage.
	SynthesizeTo(ctx context.Context, e Utterance, out io.Writer) (int, error)
	// SampleRate est connu avant toute synthèse : les en-têtes doivent
	// partir avant le premier octet d'audio.
	SampleRate() int
}

// flushWriter pousse chaque écriture vers le client au lieu de la laisser
// dormir dans le tampon HTTP. Sans ça, un streaming n'en est pas un : le client
// recevrait tout d'un bloc à la fin.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (e flushWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if e.f != nil {
		e.f.Flush()
	}
	return n, err
}

// SpeechRequest est le corps accepté par /v1/audio/speech. Les champs Model,
// LangCode et Stream existent pour ne pas faire échouer les clients OpenAI qui
// les envoient systématiquement : ils sont lus puis ignorés — un seul modèle,
// une seule langue, et la réponse est toujours renvoyée d'un bloc.
//
// Voice, Speed, Pitch et Effects sont ceux de /say : Pitch et Effects sont des
// extensions maison, absentes de l'API OpenAI, sans effet si on les omet.
type SpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	Speed          float64  `json:"speed"`
	ResponseFormat string   `json:"response_format"`
	LangCode       string   `json:"lang_code"`
	Stream         bool     `json:"stream"`
	Pitch          float64  `json:"pitch"`
	Effects        []Effect `json:"effects"`
}

// speechFormats liste les formats de sortie servis. Le moteur produit du PCM :
// tout le reste demanderait un encodeur de plus dans la chaîne, on préfère le
// dire franchement plutôt que servir du WAV sous une étiquette mp3.
var speechFormats = []string{"wav", "pcm"}

// speech monte la route de synthèse compatible OpenAI (celle de Kokoro-FastAPI
// et de l'API /v1/audio/speech d'OpenAI) : elle renvoie l'audio dans le corps
// de la réponse, sans passer par la file de lecture locale.
func speech(synth Synthesizer, s settings) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if synth == nil {
			respondError(w, http.StatusServiceUnavailable, "speech synthesis unavailable on this service")
			return
		}

		body, err := io.ReadAll(io.LimitReader(req.Body, maxTextSize))
		if err != nil {
			respondError(w, http.StatusBadRequest, string(errUnreadableText))
			return
		}
		var payload SpeechRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			respondError(w, http.StatusBadRequest, string(errInvalidJSON))
			return
		}

		text := strings.TrimSpace(payload.Input)
		if text == "" {
			respondError(w, http.StatusBadRequest, "no text provided: expected {\"input\":\"…\"}")
			return
		}

		format := strings.ToLower(strings.TrimSpace(payload.ResponseFormat))
		if format == "" {
			format = "wav"
		}
		if format != "wav" && format != "pcm" {
			respondError(w, http.StatusBadRequest, fmt.Sprintf(
				"format %q not served — available formats: %s",
				payload.ResponseFormat, strings.Join(speechFormats, ", ")))
			return
		}

		// freeVoice : un client OpenAI envoie le nom de voix de son fournisseur
		// habituel ("alloy", "ff_siwis"), qui n'a aucune raison d'exister ici.
		utterance, err := s.normalize(Utterance{
			Text:    text,
			Voice:   strings.TrimSpace(payload.Voice),
			Speed:   payload.Speed,
			Pitch:   payload.Pitch,
			Effects: payload.Effects,
		}, true)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}

		sampleRate := synth.SampleRate()
		mimeType := "audio/wav"
		if format == "pcm" {
			mimeType = fmt.Sprintf("audio/pcm; rate=%d; channels=1; bits=16", sampleRate)
		}
		// La voix effectivement utilisée peut différer de celle demandée : sans
		// cet en-tête, un client qui envoie un nom inconnu n'a aucun moyen de
		// s'en apercevoir.
		w.Header().Set("X-Voice-Used", utterance.Voice)
		w.Header().Set("X-Sample-Rate", strconv.Itoa(sampleRate))
		w.Header().Set("Content-Type", mimeType)

		if payload.Stream {
			// Les en-têtes partent avant le premier octet d'audio : à partir
			// d'ici on ne peut plus changer le code de statut, une erreur de
			// synthèse ne peut donc qu'être journalisée.
			flusher, _ := w.(http.Flusher)
			w.WriteHeader(http.StatusOK)
			out := io.Writer(flushWriter{w: w, f: flusher})
			if format == "wav" {
				if _, err := out.Write(streamingWavHeader(sampleRate)); err != nil {
					return
				}
			}
			if _, err := synth.SynthesizeTo(req.Context(), utterance, out); err != nil {
				log.Printf("streaming interrupted: %v", err)
			}
			return
		}

		var pcm bytes.Buffer
		if _, err := synth.SynthesizeTo(req.Context(), utterance, &pcm); err != nil {
			if errors.Is(err, context.Canceled) || req.Context().Err() != nil {
				log.Printf("synthesis aborted: client disconnected")
				return // plus personne pour lire une erreur
			}
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("synthesis failed: %v", err))
			return
		}
		if pcm.Len() == 0 {
			respondError(w, http.StatusInternalServerError, "no audio produced")
			return
		}

		audio := pcm.Bytes()
		if format == "wav" {
			audio = wrapWav(audio, sampleRate)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(audio)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(audio)
	}
}
