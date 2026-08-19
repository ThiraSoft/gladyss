package main

import (
	"bytes"
	"io"
	"time"
)

const (
	// probeAudio est la quantité d'audio observée avant de mesurer le débit de
	// génération. Assez pour lisser la granularité des morceaux (~30 ms), assez
	// peu pour que l'attente reste inaudible quand la lecture peut démarrer tout
	// de suite.
	probeAudio = 400 * time.Millisecond

	// charsPerSecond estime la durée d'un énoncé à partir de sa longueur. Le
	// français tenu par le modèle tourne entre 9 et 12 caractères par seconde
	// d'audio ; on retient la borne basse, qui surestime la durée — se tromper
	// vers le haut coûte un peu d'avance en trop, se tromper vers le bas coûte
	// une saccade.
	charsPerSecond = 10.0

	// priorMargin est la marge exigée d'un débit hérité de l'énoncé précédent
	// pour se passer de mesure : le débit du daemon est stable d'un énoncé à
	// l'autre, mais pas au point de jouer sur le fil.
	priorMargin = 0.85

	// maxLead borne l'avance exigée : au-delà, l'attente est plus pénible que la
	// saccade qu'elle évite.
	maxLead = 4 * time.Second

	bytesPerSample = 2
)

// pacedWriter décide quand lâcher l'audio du daemon vers le lecteur.
//
// Le lecteur consomme son entrée `drain` fois plus vite que le temps réel (le
// facteur de vitesse : la chaîne atempo/asetrate avale `drain` seconde d'audio
// par seconde de lecture). Le daemon, lui, produit `rate` fois le temps réel.
//
//   - rate ≥ drain : la production suit, on joue au fil de l'eau. Latence = le
//     temps d'observer le débit.
//   - rate < drain : le lecteur prend (drain − rate) secondes de retard par
//     seconde d'audio produite. Sur un énoncé de D secondes il faut donc
//     D·(1 − rate/drain) secondes d'avance avant de démarrer, sans quoi il
//     s'affame en cours de route.
//
// D est estimé sur la longueur du texte : c'est la seule information disponible
// avant la fin de la génération, et l'estimation n'a qu'à être du bon ordre —
// l'avance calculée est bornée par maxLead de toute façon.
type pacedWriter struct {
	out        io.Writer
	sampleRate int
	drain      float64       // vitesse de consommation du lecteur, en × temps réel
	estimated  time.Duration // durée d'audio attendue pour l'énoncé
	now        func() time.Time

	start     time.Time // première arrivée d'audio
	measured  int       // octets reçus depuis start, hors premier morceau
	buffered  bytes.Buffer
	rate      float64       // débit de génération mesuré, en × temps réel
	lead      time.Duration // avance à constituer ; -1 tant qu'elle est inconnue
	streaming bool
}

// newPacedWriter arme le régulateur. prior est le débit observé au dernier
// énoncé, ou 0 s'il n'y en a pas eu : quand il dépasse largement la vitesse de
// lecture, la mesure est inutile et le premier morceau part directement — la
// génération du daemon ne change pas de régime d'un énoncé à l'autre.
func newPacedWriter(out io.Writer, sampleRate int, drain float64, text string, prior float64) *pacedWriter {
	return &pacedWriter{
		out:        out,
		sampleRate: sampleRate,
		drain:      drain,
		estimated:  estimateDuration(text),
		now:        time.Now,
		lead:       -1,
		streaming:  prior > 0 && drain <= prior*priorMargin,
	}
}

// estimateDuration devine la durée d'audio d'un texte à partir de sa longueur.
func estimateDuration(text string) time.Duration {
	seconds := float64(len([]rune(text))) / charsPerSecond
	return time.Duration(seconds * float64(time.Second))
}

// duration convertit une quantité de PCM en durée d'écoute.
func (w *pacedWriter) duration(size int) time.Duration {
	return time.Duration(float64(size) / bytesPerSample / float64(w.sampleRate) * float64(time.Second))
}

func (w *pacedWriter) Write(p []byte) (int, error) {
	if w.streaming {
		return w.out.Write(p)
	}

	n, _ := w.buffered.Write(p)

	// Le premier morceau paie l'amorçage du modèle : le compter fausserait la
	// mesure du débit vers le bas. La mesure part de son arrivée.
	if w.start.IsZero() {
		w.start = w.now()
		return n, nil
	}
	w.measured += len(p)

	if w.lead < 0 {
		if w.duration(w.measured) < probeAudio {
			return n, nil
		}
		w.rate = w.observedRate()
		w.lead = w.requiredLead()
	}

	if w.duration(w.buffered.Len()) < w.lead {
		return n, nil
	}
	w.streaming = true
	_, err := w.buffered.WriteTo(w.out)
	return n, err
}

// observedRate est le débit de génération mesuré, en × temps réel.
func (w *pacedWriter) observedRate() float64 {
	elapsed := w.now().Sub(w.start)
	if elapsed <= 0 {
		return 0
	}
	return w.duration(w.measured).Seconds() / elapsed.Seconds()
}

// requiredLead traduit le débit observé en avance à constituer.
func (w *pacedWriter) requiredLead() time.Duration {
	if w.rate <= 0 || w.rate >= w.drain {
		return 0
	}
	lead := time.Duration(float64(w.estimated) * (1 - w.rate/w.drain))
	if lead > maxLead {
		lead = maxLead
	}
	return lead
}

// Flush pousse ce qui reste : un énoncé trop court pour atteindre l'avance visée
// n'a jamais démarré.
func (w *pacedWriter) Flush() error {
	if w.streaming || w.buffered.Len() == 0 {
		return nil
	}
	w.streaming = true
	_, err := w.buffered.WriteTo(w.out)
	return err
}
