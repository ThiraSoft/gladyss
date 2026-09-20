package main

import (
	"errors"
	"io"
	"log"
	"net/http"

	"gladyss/oral"
)

// maxStreamSize borne un flux entier, là où maxTextSize borne un énoncé : un
// tour de parole écrit au fil de l'eau est plus long qu'un énoncé, sans être
// pour autant sans fin.
const maxStreamSize = 10 * maxTextSize

// morceauLu est la taille d'une bouchée de corps. Elle n'a pas à être grande :
// ce qui compte est de rendre la main dès qu'une phrase est complète.
const morceauLu = 4 << 10

// sayStream lit le texte au fil de son arrivée et met chaque phrase en file
// dès qu'elle est finie, au lieu d'attendre le point final du tour. C'est ce
// qui permet à un client de verser les mots d'un modèle dans le corps de la
// requête pendant qu'il les écrit : la voix démarre sur la première phrase.
//
// Le corps se donne en clair, en morceaux (Transfer-Encoding: chunked) ; les
// réglages passent en paramètres de requête, puisqu'ils valent pour tout le
// flux. « ?filter=off » rend le texte entier, sans le tri du narrateur.
func sayStream(c *Controller, s settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		voice, speed, pitch, effects, err := extractOptions(r)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}

		modele := Utterance{Voice: voice, Speed: speed, Pitch: pitch, Effects: effects}
		// Les réglages sont jugés avant le premier mot : une voix inconnue doit
		// se voir tout de suite, pas une fois la moitié du tour déjà en file.
		temoin := modele
		temoin.Text = "a"
		temoin, err = s.normalize(temoin, false)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Les défauts du service sont posés une fois pour tout le flux : c'est
		// aussi ce que la réponse annonce avoir prononcé.
		modele.Voice, modele.Speed, modele.Pitch = temoin.Voice, temoin.Speed, temoin.Pitch

		options := oral.Options{Narrator: oral.NewRuleNarrator(), Min: oral.Seuil}
		if r.URL.Query().Get("filter") == "off" {
			options.Narrator = nil
		}
		flux := oral.New(options)

		var mis, position int
		enfiler := func(enonces []string) {
			for _, enonce := range enonces {
				u := modele
				u.Text = enonce
				u, err := s.normalize(u, false)
				if err != nil {
					continue // rien de prononçable : l'énoncé suivant vaut mieux qu'une erreur
				}
				position = c.Enqueue(u)
				mis++
			}
		}

		corps := io.LimitReader(r.Body, maxStreamSize)
		morceau := make([]byte, morceauLu)
		for {
			n, err := corps.Read(morceau)
			if n > 0 {
				enfiler(flux.Push(string(morceau[:n])))
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					// Le client coupé en plein tour est un cas normal : ce qui
					// était complet est déjà en file, on dit le reste.
					log.Printf("/say/stream: %v", err)
				}
				break
			}
		}
		enfiler(flux.Flush())

		respondJSON(w, http.StatusAccepted, map[string]any{
			"enqueued": mis,
			"position": position,
			"voice":    temoin.Voice,
		})
	}
}
