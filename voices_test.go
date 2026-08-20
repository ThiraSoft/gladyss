package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThiraSoft/golem/pockettts"
)

// catalogueDeTest monte un répertoire voix/ jetable avec les fichiers demandés.
func catalogueDeTest(t *testing.T, fichiers ...string) *voiceCatalog {
	t.Helper()
	dir := t.TempDir()
	for _, f := range fichiers {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lang, err := pockettts.LookupLanguage("french_24l")
	if err != nil {
		t.Fatal(err)
	}
	return newVoiceCatalog(dir, lang)
}

func TestResolveVoixLocale(t *testing.T) {
	c := catalogueDeTest(t, "gladyss.safetensors")
	got, err := c.resolve("gladyss")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.path) != "gladyss.safetensors" {
		t.Errorf("resolve = %q, attendu le fichier local", got.path)
	}
	if got.clone {
		t.Error("un état précalculé n'a pas à être encodé")
	}
	if !strings.HasPrefix(got.path, c.dir) {
		t.Errorf("resolve = %q, attendu sous %q", got.path, c.dir)
	}
}

// Un WAV sans état précalculé est une voix à cloner : le moteur l'encodera au
// premier usage et mettra le résultat en cache à côté.
func TestResolveWavSeul(t *testing.T) {
	c := catalogueDeTest(t, "essai.wav")
	got, err := c.resolve("essai")
	if err != nil {
		t.Fatal(err)
	}
	if !got.clone {
		t.Error("attendu un enregistrement à encoder")
	}
	if filepath.Base(got.path) != "essai.wav" {
		t.Errorf("resolve = %q, attendu le WAV", got.path)
	}
}

// L'état précalculé l'emporte sur le WAV dont il vient : c'est tout l'intérêt
// du cache, et le WAV n'a plus à être relu.
func TestResolvePrefereLEtatAuWav(t *testing.T) {
	c := catalogueDeTest(t, "essai.wav", "essai.safetensors")
	got, err := c.resolve("essai")
	if err != nil {
		t.Fatal(err)
	}
	if got.clone {
		t.Errorf("resolve = %+v, attendu l'état précalculé", got)
	}
}

// Une voix n'existant que sous forme de WAV est tout de même annoncée : elle
// est utilisable, au prix d'un encodage.
func TestNamesContientLesWav(t *testing.T) {
	c := catalogueDeTest(t, "essai.wav")
	for _, n := range c.names() {
		if n == "essai" {
			return
		}
	}
	t.Errorf("names = %v, attendu qu'il contienne essai", c.names())
}

func TestResolveInconnue(t *testing.T) {
	c := catalogueDeTest(t)
	_, err := c.resolve("personne")
	if err == nil {
		t.Fatal("attendu une erreur pour une voix inconnue")
	}
	if !strings.Contains(err.Error(), "personne") {
		t.Errorf("erreur = %q, attendu qu'elle nomme la voix", err)
	}
}

// Le catalogue mêle les voix locales et celles de Kyutai, sans doublon et dans
// l'ordre. Les secondes peuvent manquer sur une machine sans modèle : le test
// ne vérifie que les locales, l'ordre et l'absence de doublon.
func TestNamesContientLesVoixLocales(t *testing.T) {
	c := catalogueDeTest(t, "gladyss.safetensors", "zoe.safetensors")
	noms := c.names()
	trouve := map[string]bool{}
	for _, n := range noms {
		trouve[n] = true
	}
	if !trouve["gladyss"] || !trouve["zoe"] {
		t.Errorf("names = %v, attendu qu'il contienne gladyss et zoe", noms)
	}
	for i := 1; i < len(noms); i++ {
		if noms[i-1] > noms[i] {
			t.Fatalf("names = %v, attendu trié", noms)
		}
		if noms[i-1] == noms[i] {
			t.Fatalf("names = %v, attendu sans doublon", noms)
		}
	}
}
