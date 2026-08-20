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
	if filepath.Base(got) != "gladyss.safetensors" {
		t.Errorf("resolve = %q, attendu le fichier local", got)
	}
	if !strings.HasPrefix(got, c.dir) {
		t.Errorf("resolve = %q, attendu sous %q", got, c.dir)
	}
}

// Un WAV sans état précalculé n'est pas utilisable : le clonage demande
// l'encodeur Mimi, qui viendra plus tard. L'erreur doit le dire.
func TestResolveWavSeul(t *testing.T) {
	c := catalogueDeTest(t, "essai.wav")
	_, err := c.resolve("essai")
	if err == nil {
		t.Fatal("attendu une erreur pour un WAV sans .safetensors")
	}
	if !strings.Contains(err.Error(), "essai.wav") {
		t.Errorf("erreur = %q, attendu qu'elle nomme le WAV", err)
	}
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
