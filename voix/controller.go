package voix

import (
	"context"
	"sync"
)

// Enonce est une demande de lecture : un texte et la voix qui doit le prononcer.
type Enonce struct {
	Text  string `json:"text"`
	Voice string `json:"voice,omitempty"`
	// Speed est un facteur de tempo ; 0 signifie « celle du service ».
	Speed float64 `json:"speed,omitempty"`
	// Pitch est un facteur de hauteur ; 0 signifie « celle du service ».
	Pitch float64 `json:"pitch,omitempty"`
	// Effects sont appliqués dans l'ordre, après la hauteur et le tempo.
	Effects []Effect `json:"effects,omitempty"`
}

// Parleur prononce un énoncé de bout en bout. L'implémentation doit interrompre
// la lecture dès que le contexte est annulé.
type Parleur interface {
	Speak(ctx context.Context, e Enonce) error
}

// Controleur sérialise les demandes de lecture : une seule à la fois, dans l'ordre d'arrivée.
type Controleur struct {
	sp Parleur

	mu      sync.Mutex
	queue   []Enonce
	current Enonce
	// cancel interrompt l'énoncé en cours de lecture ; nil quand rien n'est lu.
	cancel context.CancelFunc

	wake chan struct{}
	quit chan struct{}
	wg   sync.WaitGroup
}

func NouveauControleur(sp Parleur) *Controleur {
	return &Controleur{
		sp:   sp,
		wake: make(chan struct{}, 1),
		quit: make(chan struct{}),
	}
}

// Start démarre le worker qui vide la file séquentiellement.
func (c *Controleur) Start() {
	c.wg.Add(1)
	go c.loop()
}

// Enqueue ajoute un énoncé en fin de file et renvoie son rang d'attente
// (1 = prochain énoncé à être lu).
func (c *Controleur) Enqueue(e Enonce) int {
	c.mu.Lock()
	c.queue = append(c.queue, e)
	position := len(c.queue)
	if c.current.Text != "" {
		position++ // l'énoncé déjà au micro passe avant celui-ci
	}
	c.mu.Unlock()
	c.wakeUp()
	return position
}

// Close arrête le worker en interrompant la lecture en cours.
func (c *Controleur) Close() {
	close(c.quit)
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.wakeUp() // débloque le worker s'il attend un nouvel énoncé
	c.wg.Wait()
}

// Etat est une photographie de la file, destinée à l'exposition HTTP.
type Etat struct {
	Current Enonce   `json:"current"`
	Pending []Enonce `json:"pending"`
}

// Snapshot renvoie l'état courant de la file.
func (c *Controleur) Snapshot() Etat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Etat{
		Current: c.current,
		Pending: append([]Enonce{}, c.queue...),
	}
}

// Stop interrompt l'énoncé en cours et purge la file.
// Renvoie le nombre d'énoncés retirés de la file (l'énoncé en cours n'est pas compté).
func (c *Controleur) Stop() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := len(c.queue)
	c.queue = nil
	if c.cancel != nil {
		c.cancel()
	}
	return removed
}

// Skip interrompt l'énoncé en cours. Renvoie false si rien n'était en cours de lecture.
func (c *Controleur) Skip() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil {
		return false
	}
	c.cancel()
	return true
}

func (c *Controleur) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default: // un réveil est déjà en attente, inutile d'en empiler un second
	}
}

func (c *Controleur) dequeue() (Enonce, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return Enonce{}, false
	}
	e := c.queue[0]
	c.queue = c.queue[1:]
	// current est posé ici, sous le même verrou que le retrait : sans cela un
	// énoncé serait invisible (ni en file, ni en cours) le temps du transfert,
	// et les positions annoncées seraient sous-évaluées.
	c.current = e
	return e, true
}

func (c *Controleur) loop() {
	defer c.wg.Done()
	for {
		select {
		case <-c.quit:
			return
		default:
		}

		e, ok := c.dequeue()
		if !ok {
			select {
			case <-c.wake:
			case <-c.quit:
				return
			}
			continue
		}

		c.runUtterance(e)
	}
}

// runUtterance prononce un énoncé en le rendant interruptible par Skip.
func (c *Controleur) runUtterance(e Enonce) {
	ctx, cancel := context.WithCancel(context.Background())

	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.current = Enonce{}
		c.mu.Unlock()
		cancel()
	}()

	_ = c.sp.Speak(ctx, e)
}
