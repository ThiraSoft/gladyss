package main

import (
	"io"
	"sync"
	"time"
)

const (
	// primingTick est la cadence à laquelle le silence d'amorçage est versé au
	// lecteur. Assez court pour que le passage au son réel ne se fasse jamais
	// attendre, assez long pour que le réveil ne coûte pas un tour de
	// planificateur par milliseconde.
	primingTick = 20 * time.Millisecond

	// primingCushion est le silence glissé juste avant les premiers échantillons
	// de parole. Le flux d'amorçage s'arrête net à cet instant : le coussin
	// couvre l'écart entre le dernier tick versé et l'arrivée de la voix, pour
	// que la sortie ne se vide pas entre les deux.
	primingCushion = 30 * time.Millisecond
)

// primingWriter tient le lecteur — et surtout le périphérique de sortie —
// éveillé tant que la génération n'a rien à jouer.
//
// Le PCM produit par le moteur est complet : la voix y est entière. Ce qui
// manque à l'écoute se perd en aval, au démarrage de la chaîne audio — ouverture
// du périphérique, réveil d'un casque USB ou d'un ampli — qui avale les premiers
// dixièmes de seconde, parfois plusieurs secondes sur un matériel au repos.
// Comme le lecteur est lancé avec une entrée vide, ce réveil ne commence qu'à
// l'arrivée du premier échantillon, c'est-à-dire pile sur le premier mot.
//
// L'amorçage déplace ce réveil : dès le lancement du lecteur, du silence part à
// la cadence de consommation. Le périphérique s'ouvre sur du silence, l'avale, et
// la voix arrive sur une chaîne déjà chaude. Le silence ne retarde rien — il ne
// remplit que le temps où il n'y avait, de toute façon, rien à jouer : il se coupe
// au premier octet de parole, sous mutex, donc jamais au milieu de la voix.
type primingWriter struct {
	out        io.Writer
	sampleRate int
	drain      float64 // vitesse de consommation du lecteur, en × temps réel

	mu       sync.Mutex
	speaking bool // la parole a commencé : plus un octet de silence

	stop    chan struct{}
	done    chan struct{}
	started bool
}

func newPrimingWriter(out io.Writer, sampleRate int, drain float64) *primingWriter {
	return &primingWriter{
		out:        out,
		sampleRate: sampleRate,
		drain:      drain,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// silenceSize rend la quantité de PCM qu'un lecteur consommant `drain` fois le
// temps réel avale pendant la durée d'horloge d.
func silenceSize(d time.Duration, sampleRate int, drain float64) int {
	samples := int(d.Seconds() * drain * float64(sampleRate))
	return samples * bytesPerSample
}

// start lance le versement du silence. À arrêter par Close avant de fermer la
// destination.
func (w *primingWriter) start() {
	w.started = true
	go func() {
		defer close(w.done)
		t := time.NewTicker(primingTick)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				// Une écriture qui échoue signe un lecteur parti : le contexte a
				// été annulé, ou le processus est mort. Il n'y a plus rien à amorcer.
				if err := w.tick(); err != nil {
					return
				}
			}
		}
	}()
}

// tick verse un tour de silence, sauf si la parole a déjà commencé.
func (w *primingWriter) tick() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.speaking {
		return nil
	}
	return w.writeSilence(primingTick)
}

func (w *primingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.speaking {
		w.speaking = true
		if err := w.writeSilence(primingCushion); err != nil {
			return 0, err
		}
	}
	return w.out.Write(p)
}

// writeSilence est appelée sous mu.
func (w *primingWriter) writeSilence(d time.Duration) error {
	n := silenceSize(d, w.sampleRate, w.drain)
	if n <= 0 {
		return nil
	}
	_, err := w.out.Write(make([]byte, n))
	return err
}

// Close arrête le versement et attend que la dernière écriture soit sortie :
// après lui, plus personne ne touche à la destination.
func (w *primingWriter) Close() error {
	if !w.started {
		return nil
	}
	w.started = false
	close(w.stop)
	<-w.done
	return nil
}
