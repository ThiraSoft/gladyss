package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// daemonFactice remplace le daemon Python : il lit les commandes sur un tube et
// répond avec le protocole ligne-JSON, sous le contrôle du test.
//
// Il ne lit que lorsque le test le demande. C'est essentiel : le vrai daemon est
// occupé à générer, donc une commande écrite pendant ce temps reste en suspens
// dans le tube. Un faux daemon qui lirait en continu rendrait toute écriture
// instantanée et masquerait les problèmes d'ordre d'émission.
type daemonFactice struct {
	commandes *bufio.Reader
	sortie    *io.PipeWriter
}

// moteurFactice monte un PocketTTS branché sur des tubes en mémoire. Les tubes
// sont non tamponnés : une écriture bloque jusqu'à sa lecture, ce qui rend les
// ordres d'émission observables — c'est tout l'objet du test.
func moteurFactice(t *testing.T) (*PocketTTS, *daemonFactice) {
	t.Helper()
	lecturesCmd, ecrituresCmd := io.Pipe()
	lecturesOut, ecrituresOut := io.Pipe()

	d := &daemonFactice{commandes: bufio.NewReader(lecturesCmd), sortie: ecrituresOut}

	p := &PocketTTS{
		stdin:        ecrituresCmd,
		stdout:       bufio.NewReader(lecturesOut),
		sampleRate:   24000,
		defaultVoice: "estelle",
		defaultSpeed: 1,
		defaultPitch: 1,
	}
	return p, d
}

func (d *daemonFactice) emettre(t *testing.T, entete string, charge []byte) {
	t.Helper()
	if _, err := d.sortie.Write(append([]byte(entete), '\n')); err != nil {
		t.Fatalf("émission de %s : %v", entete, err)
	}
	if len(charge) > 0 {
		if _, err := d.sortie.Write(charge); err != nil {
			t.Fatalf("émission de la charge : %v", err)
		}
	}
}

// prochaineCommande consomme une commande, débloquant du même coup son écriture.
func (d *daemonFactice) prochaineCommande(t *testing.T) map[string]string {
	t.Helper()
	return d.lire(t, 2*time.Second)
}

// aucuneCommande vérifie qu'aucune commande n'attend dans le tube.
func (d *daemonFactice) aucuneCommande(t *testing.T, patience time.Duration) {
	t.Helper()
	if cmd := d.lire(t, patience); cmd != nil {
		t.Errorf("commande inattendue sur le tube : %v", cmd)
	}
}

// lire renvoie la prochaine commande, ou nil au bout de patience. Un t.Fatal
// signale l'absence de commande là où le test en attend une.
func (d *daemonFactice) lire(t *testing.T, patience time.Duration) map[string]string {
	t.Helper()
	type resultat struct {
		cmd map[string]string
		err error
	}
	recu := make(chan resultat, 1)
	go func() {
		ligne, err := d.commandes.ReadBytes('\n')
		var cmd map[string]string
		if err == nil {
			err = json.Unmarshal(ligne, &cmd)
		}
		recu <- resultat{cmd, err}
	}()
	select {
	case r := <-recu:
		if r.err != nil {
			t.Fatalf("lecture de commande : %v", r.err)
		}
		return r.cmd
	case <-time.After(patience):
		return nil
	}
}

func TestSynthetiserVersNeLaisseAucunCancelFuirApresSonRetour(t *testing.T) {
	p, d := moteurFactice(t)

	ctx, annuler := context.WithCancel(context.Background())
	rendu := make(chan error, 1)
	go func() {
		_, err := p.SynthesizeTo(ctx, Utterance{Text: "bonjour"}, io.Discard)
		rendu <- err
	}()

	if cmd := d.prochaineCommande(t); cmd["cmd"] != "say" {
		t.Fatalf("première commande = %v, want say", cmd)
	}
	d.emettre(t, `{"type":"audio","bytes":2}`, []byte{1, 2})

	// Le client se déconnecte : le contexte tombe. Le « cancel » part vers le
	// daemon, qui ne le lit pas encore — l'écriture reste donc en suspens. La
	// pause laisse la surveillance atteindre cette écriture avant la fin de
	// l'énoncé : sans elle, le select pourrait voir l'arrêt et l'annulation en
	// même temps et n'émettre aucun cancel, ce qui ne testerait plus rien.
	annuler()
	time.Sleep(100 * time.Millisecond)
	d.emettre(t, `{"type":"end","cancelled":true}`, nil)

	// L'invariant : tant que le « cancel » est en suspens, la synthèse ne rend
	// pas la main — donc ne relâche pas le tube. Sinon ce cancel atterrirait au
	// milieu de l'énoncé suivant et le couperait.
	select {
	case <-rendu:
		t.Fatal("la synthèse a rendu la main avec un cancel encore en suspens : il coupera l'énoncé suivant")
	case <-time.After(200 * time.Millisecond):
	}

	if cmd := d.prochaineCommande(t); cmd["cmd"] != "cancel" {
		t.Fatalf("deuxième commande = %v, want cancel", cmd)
	}
	select {
	case <-rendu:
	case <-time.After(2 * time.Second):
		t.Fatal("la synthèse ne rend pas la main après l'émission du cancel")
	}
}

func TestSynthetiserVersEnchaineDeuxEnoncesSansSeCouper(t *testing.T) {
	p, d := moteurFactice(t)

	for i, texte := range []string{"premier", "second"} {
		ctx, annuler := context.WithCancel(context.Background())
		rendu := make(chan struct{})
		go func() {
			_, _ = p.SynthesizeTo(ctx, Utterance{Text: texte}, io.Discard)
			close(rendu)
		}()

		cmd := d.prochaineCommande(t)
		if cmd["cmd"] != "say" || cmd["text"] != texte {
			t.Fatalf("énoncé %d : commande = %v, want say %q", i+1, cmd, texte)
		}
		d.emettre(t, `{"type":"audio","bytes":2}`, []byte{1, 2})
		d.emettre(t, `{"type":"end"}`, nil)
		<-rendu
		annuler() // déconnexion après coup : plus aucun cancel ne doit partir
	}

	// Rien d'autre ne doit avoir circulé : pas de cancel intercalé entre les deux.
	d.aucuneCommande(t, 200*time.Millisecond)
}

// TestNewPocketTTSTransmetLaVoixParDefautAuDaemon vérifie que `-voice` atteint le
// daemon, qui s'en sert pour choisir la voix à précharger. Le faux daemon renvoie
// la variable d'environnement reçue dans son catalogue : c'est le seul moyen de
// l'observer depuis le service, et un préchargement de la mauvaise voix ne se
// verrait autrement que sur la latence du premier énoncé.
func TestNewPocketTTSTransmetLaVoixParDefautAuDaemon(t *testing.T) {
	script := filepath.Join(t.TempDir(), "faux_daemon.sh")
	contenu := `printf '{"type":"ready","sample_rate":24000,"voices":["%s"]}\n' "$SAY_DEFAULT_VOICE"
cat > /dev/null
`
	if err := os.WriteFile(script, []byte(contenu), 0o700); err != nil {
		t.Fatalf("écriture du faux daemon : %v", err)
	}

	p, err := NewPocketTTS("/bin/sh", script, "gladyss", "true", "true", 1, 1, defaultEOSThreshold)
	if err != nil {
		t.Fatalf("NewPocketTTS() erreur = %v", err)
	}
	defer p.Close()

	voix := p.Voices()
	if len(voix) != 1 || voix[0] != "gladyss" {
		t.Errorf("catalogue = %v, want [gladyss] — SAY_DEFAULT_VOICE n'a pas atteint le daemon", voix)
	}
}

// TestNewPocketTTSTransmetLeSeuilEOSAuDaemon vérifie que `-eos-threshold` atteint
// le daemon. Le seuil décide quand le modèle s'arrête de parler : au défaut de la
// bibliothèque (-4,0), une phrase de quatre mots est coupée en pleine syllabe une
// fois sur deux. Un seuil qui n'arriverait pas jusqu'au modèle ne se verrait que
// sur des troncatures intermittentes, donc très tard.
func TestNewPocketTTSTransmetLeSeuilEOSAuDaemon(t *testing.T) {
	script := filepath.Join(t.TempDir(), "faux_daemon.sh")
	contenu := `printf '{"type":"ready","sample_rate":24000,"voices":["%s"]}\n' "$SAY_EOS_THRESHOLD"
cat > /dev/null
`
	if err := os.WriteFile(script, []byte(contenu), 0o700); err != nil {
		t.Fatalf("écriture du faux daemon : %v", err)
	}

	p, err := NewPocketTTS("/bin/sh", script, "gladyss", "true", "true", 1, 1, -1.5)
	if err != nil {
		t.Fatalf("NewPocketTTS() erreur = %v", err)
	}
	defer p.Close()

	if voix := p.Voices(); len(voix) != 1 || voix[0] != "-1.5" {
		t.Errorf("catalogue = %v, want [-1.5] — SAY_EOS_THRESHOLD n'a pas atteint le daemon", voix)
	}
}
