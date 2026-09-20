package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// flux ouvre une requête dont le corps arrive au fil de l'eau, comme le ferait
// un client qui y verse les mots d'un modèle pendant qu'il écrit.
func flux(t *testing.T, h http.Handler, cible string) (*io.PipeWriter, func() *httptest.ResponseRecorder) {
	t.Helper()
	pr, pw := io.Pipe()
	req := httptest.NewRequest(http.MethodPost, cible, pr)
	rec := httptest.NewRecorder()
	fini := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(fini)
	}()
	return pw, func() *httptest.ResponseRecorder {
		select {
		case <-fini:
		case <-time.After(2 * time.Second):
			t.Fatal("le handler n'a pas rendu la main")
		}
		return rec
	}
}

// attendreEnonces attend que n énoncés aient été prononcés jusqu'au bout.
func attendreEnonces(t *testing.T, sp *fakeSpeaker, n int) []string {
	t.Helper()
	for range 200 {
		if dits := sp.prononces(); len(dits) >= n {
			return dits
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d énoncés prononcés, want %d", len(sp.prononces()), n)
	return nil
}

// Le point de tout l'exercice : la première phrase est dite alors que la
// suivante n'est même pas écrite.
func TestSayStreamParleAvantLaFinDuTexte(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 50*time.Millisecond)

	corps, attendre := flux(t, h, "/say/stream")
	if _, err := corps.Write([]byte("Voici une première phrase, bien assez longue pour partir seule. ")); err != nil {
		t.Fatalf("écriture: %v", err)
	}
	if got := sp.attendDemarrage(t); got != "Voici une première phrase, bien assez longue pour partir seule." {
		t.Fatalf("énoncé lu = %q, want la première phrase, dite sans attendre la suite", got)
	}

	if _, err := corps.Write([]byte("Et la suite, écrite bien après que la première a commencé à être dite.")); err != nil {
		t.Fatalf("écriture: %v", err)
	}
	corps.Close()

	rec := attendre()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	var reponse struct {
		Enqueued int    `json:"enqueued"`
		Voice    string `json:"voice"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reponse); err != nil {
		t.Fatalf("réponse illisible: %v — corps: %s", err, rec.Body)
	}
	if reponse.Enqueued != 2 {
		t.Errorf("enqueued = %d, want 2", reponse.Enqueued)
	}
	// La voix rendue est celle qui a parlé, défaut du service compris.
	if reponse.Voice != "estelle" {
		t.Errorf("voice = %q, want \"estelle\" (la voix par défaut du service)", reponse.Voice)
	}
	if got := sp.attendDemarrage(t); !strings.HasPrefix(got, "Et la suite") {
		t.Errorf("second énoncé = %q", got)
	}
}

func TestSayStreamNeLitPasLesBlocsDeCode(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 20*time.Millisecond)

	corps, attendre := flux(t, h, "/say/stream")
	corps.Write([]byte("Voici la commande à lancer, elle tient sur une seule ligne et se passe de commentaire.\n"))
	corps.Write([]byte("```go\nfmt.Println(\"bonjour\")\n```\n"))
	corps.Write([]byte("Et voilà, c'est tout ce qu'il y avait à faire aujourd'hui, rien de plus à ajouter.\n"))
	corps.Close()
	attendre()

	dits := attendreEnonces(t, sp, 2)
	if len(dits) != 2 {
		t.Fatalf("énoncés = %#v, want deux énoncés sans le bloc de code", dits)
	}
	for _, dit := range dits {
		if strings.Contains(dit, "Println") {
			t.Errorf("le bloc de code a été lu : %q", dit)
		}
	}
}

// ?filter=off laisse passer ce que le narrateur écarterait : l'appelant qui
// pose lui-même ses marques dans la phrase veut son texte entier.
func TestSayStreamSansFiltreDitTout(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 20*time.Millisecond)

	corps, attendre := flux(t, h, "/say/stream?filter=off")
	corps.Write([]byte("- une puce que le narrateur écarterait d'habitude.\n"))
	corps.Close()
	attendre()

	dits := attendreEnonces(t, sp, 1)
	if len(dits) != 1 || !strings.Contains(dits[0], "puce") {
		t.Errorf("énoncés = %#v, want la ligne entière", dits)
	}
}

// Une voix inconnue doit se voir tout de suite, avant que le premier mot ne
// parte en file : c'est la règle de /say, elle ne change pas ici.
func TestSayStreamRefuseUneVoixInconnue(t *testing.T) {
	h, _, _ := serveurDeTest(t, 20*time.Millisecond)

	rec := appel(t, h, http.MethodPost, "/say/stream?voice=personne", "Bonjour.")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d — corps: %s", rec.Code, http.StatusBadRequest, rec.Body)
	}
}

// Les réglages de /say valent ici : ils sont posés une fois pour tout le flux.
func TestSayStreamAppliqueLesReglagesATousLesEnonces(t *testing.T) {
	h, sp, _ := serveurDeTest(t, 20*time.Millisecond)

	corps, attendre := flux(t, h, "/say/stream?voice=alba&speed=1.3")
	corps.Write([]byte("Première phrase, assez longue pour être dite toute seule sans attendre.\n"))
	corps.Write([]byte("Seconde phrase, elle aussi bien assez longue pour partir de son côté.\n"))
	corps.Close()
	attendre()

	attendreEnonces(t, sp, 2)
	voix, vitesses := sp.voixDemandees(), sp.vitessesDemandees()
	if len(voix) != 2 {
		t.Fatalf("énoncés = %d, want 2", len(voix))
	}
	for i := range voix {
		if voix[i] != "alba" || vitesses[i] != 1.3 {
			t.Errorf("énoncé %d : voix %q vitesse %v, want alba 1.3", i, voix[i], vitesses[i])
		}
	}
}
