# Inférence en processus — plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Supprimer `tts_daemon.py` de gladyss en faisant l'inférence Pocket TTS dans le processus Go, via le paquet `pockettts` de golem.

**Architecture:** golem gagne d'abord trois ajouts d'API (réglages explicites, annulation par contexte, résolution des voix du cache Hugging Face) et sort en v0.2.0. gladyss remplace ensuite l'intérieur de `PocketTTS` — le sous-processus et son protocole JSON deviennent un `*pockettts.Engine` et un callback de frames — en gardant sa surface publique intacte, de sorte que `controller.go`, `http.go`, `pacing.go` et `lazy.go` ne bougent pas.

**Tech Stack:** Go 1.23, bibliothèque standard seule, `github.com/ThiraSoft/golem`. Pas de cgo, pas de Python.

**Spec:** `docs/superpowers/specs/2026-08-20-inference-in-process-design.md`

## Global Constraints

- **Deux dépôts.** Tâches 1 à 4 : `/home/ricardo/dev/golem`. Tâches 5 à 10 : `/home/ricardo/dev/gladyss`, branche `inference-in-process`.
- **Langue.** Dans golem, tout — code, commentaires, messages d'erreur, commits — est en anglais. Dans gladyss, les commentaires et les commits sont en français, les messages d'erreur en anglais comme le code existant.
- **Aucune dépendance externe** hors `github.com/ThiraSoft/golem` : bibliothèque standard, pas de cgo. `ffplay` et `ffmpeg` restent appelés en sous-processus, ils partiront aux chantiers 2 et 3.
- **La surface publique de `PocketTTS` ne change pas** : `Speak`, `Synthesize`, `SynthesizeTo`, `SampleRate`, `Voices`, `Close`, mêmes signatures. Aucun autre fichier de gladyss que `engine.go`, `main.go` et les tests ne doit être modifié.
- **Un test qui a besoin des poids saute** au lieu d'échouer (`t.Skip`), convention de golem : `go test ./...` reste vert sur un clone nu.
- **Le seuil de fin de parole de gladyss vaut 0.0**, et c'est une vraie valeur, pas un « non renseigné ».
- **Taux d'échantillonnage** : 24000 Hz, PCM signé 16 bits little-endian, mono.

---

## Structure des fichiers

**golem**
- Modifier `pockettts/pockettts.go` — `DefaultSettings`, `Settings.Ctx`, suppression de `Settings.defaults`
- Modifier `pockettts/languages.go` — `Language.EmbeddingPath`
- Créer `pockettts/locate.go` — `Locate`, la recherche dans le cache Hugging Face, déplacée depuis `cmd/pocket-tts/main.go`
- Modifier `pockettts/pockettts_test.go`, `pockettts/languages_test.go`, `pockettts/bench_test.go`, `cmd/pocket-tts/main.go`
- Modifier `README.md`, `pockettts/README.md`

**gladyss**
- Créer `voices.go` — résolution d'un nom de voix vers un fichier, et catalogue
- Créer `voices_test.go`
- Réécrire `engine.go` — `PocketTTS` au-dessus de `pockettts.Engine`
- Réécrire `engine_test.go`
- Supprimer `tts_daemon.py`, `requirements.txt`, `protocol_test.go`
- Modifier `main.go`, `install.sh`, `README.md`, `go.mod`

---

## Tâche 1 : golem — des réglages explicites

**Files:**
- Modify: `pockettts/pockettts.go` (`Settings`, `defaults`, `Synthesize`)
- Modify: `cmd/pocket-tts/main.go`
- Test: `pockettts/pockettts_test.go`

**Interfaces:**
- Consumes: rien
- Produces: `func DefaultSettings(lang Language) Settings`. `Settings.defaults` disparaît. `Synthesize(t string, voice *Voice, r *Settings)` garde sa signature ; `r == nil` vaut `DefaultSettings(m.lang)`, mais un `r` fourni est pris tel quel, champ par champ.

**Pourquoi :** `EndThreshold: 0` veut dire « -4 » aujourd'hui. gladyss utilise 0.0 comme vraie valeur de seuil ; sans ce changement la migration rend silencieusement le seuil du modèle et coupe la fin des phrases courtes.

- [ ] **Step 1: Write the failing test**

Dans `pockettts/pockettts_test.go` :

```go
func TestDefaultSettings(t *testing.T) {
	lang, err := LookupLanguage("french_24l")
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings(lang)
	if s.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", s.Temperature)
	}
	if s.EndThreshold != -4 {
		t.Errorf("EndThreshold = %v, want -4", s.EndThreshold)
	}
	if s.FramesAfterEnd != lang.FramesAfterEnd {
		t.Errorf("FramesAfterEnd = %v, want %v", s.FramesAfterEnd, lang.FramesAfterEnd)
	}
	if s.MaxTokens != 50 {
		t.Errorf("MaxTokens = %v, want 50", s.MaxTokens)
	}
}

// A caller that means zero gets zero. That is the whole point: gladyss sets an
// end threshold of 0.0 on purpose, and the old zero-means-default rule turned
// it into -4 without saying so.
func TestSettingsKeepsAnExplicitZero(t *testing.T) {
	lang, _ := LookupLanguage("french_24l")
	s := DefaultSettings(lang)
	s.EndThreshold = 0
	if s.EndThreshold != 0 {
		t.Errorf("EndThreshold = %v, want 0", s.EndThreshold)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pockettts -run TestDefaultSettings -v
```

Attendu : échec de compilation, `undefined: DefaultSettings`.

- [ ] **Step 3: Write minimal implementation**

Dans `pockettts/pockettts.go`, remplacer la méthode `defaults` par :

```go
// DefaultSettings returns the model's own values, which are those of the
// reference daemon. Callers start from it and change what they mean to change.
//
// The settings carry no zero-means-default rule: a caller that sets a field to
// zero gets zero. An end threshold of 0 is a legitimate setting — it makes the
// model far more reluctant to declare itself done — and the earlier rule turned
// it into -4 silently.
func DefaultSettings(lang Language) Settings {
	return Settings{
		Temperature:    0.7,
		EndThreshold:   -4,
		FramesAfterEnd: lang.FramesAfterEnd,
		MaxTokens:      50,
	}
}
```

Dans le commentaire du type `Settings`, remplacer « The zero value gives the model's own values, which are those of the reference daemon. » par « Start from DefaultSettings and change what you mean to change: no field is interpreted, and a zero is a zero. » Retirer aussi les mentions `0 -> ...` de chaque champ, qui décrivent la règle supprimée :

```go
type Settings struct {
	Temperature    float64 // the variance of the starting noise
	EndThreshold   float64 // beyond it, the model declares itself done
	FramesAfterEnd int     // enough to let the sentence settle
	MaxTokens      int     // size of a segment
	Seed           uint64  // 0 for a random draw
	...
}
```

`Seed` garde sa règle : zéro y veut dire un tirage aléatoire, ce qui est une valeur légitime et non un défaut caché.

Dans `Synthesize`, remplacer :

```go
	set := Settings{}
	if r != nil {
		set = *r
	}
	set.defaults(m.lang)
```

par :

```go
	set := DefaultSettings(m.lang)
	if r != nil {
		set = *r
	}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /home/ricardo/dev/golem && go test ./pockettts -run TestDefaultSettings -v && go test ./pockettts -run TestSettingsKeepsAnExplicitZero -v
```

Attendu : PASS.

- [ ] **Step 5: Adapter les appelants**

Chercher les constructions de `Settings` qui comptaient sur les zéros :

```bash
cd /home/ricardo/dev/golem && grep -rn "Settings{" --include=*.go .
```

Chaque site qui passait un `&Settings{Seed: x}` doit devenir `s := DefaultSettings(lang); s.Seed = x`. Dans `cmd/pocket-tts/main.go`, remplacer :

```go
	sound, err := engine.Synthesize(text, v, &pockettts.Settings{Seed: *seed})
```

par :

```go
	settings := pockettts.DefaultSettings(lang)
	settings.Seed = *seed
	sound, err := engine.Synthesize(text, v, &settings)
```

- [ ] **Step 6: Toute la suite**

```bash
cd /home/ricardo/dev/golem && go build ./... && go vet ./... && go test ./...
```

Attendu : compilation propre, tests verts (ceux qui demandent les poids sautent si absents).

- [ ] **Step 7: Commit**

```bash
cd /home/ricardo/dev/golem
git checkout -b settings-without-hidden-defaults
git add pockettts/pockettts.go pockettts/pockettts_test.go cmd/pocket-tts/main.go
git commit -m "Settings say what they mean, zero included

An end threshold of 0 is a real setting, not an unset field."
```

---

## Tâche 2 : golem — l'annulation par contexte

**Files:**
- Modify: `pockettts/pockettts.go` (`Settings`, `Synthesize`, `synthesizeSegment`)
- Test: `pockettts/pockettts_test.go`

**Interfaces:**
- Consumes: `DefaultSettings` (tâche 1)
- Produces: `Settings.Ctx context.Context`. `Synthesize` rend `ctx.Err()` — donc `context.Canceled` — dès que le contexte est annulé, en s'arrêtant à la frame suivante. Un `Ctx` nul se comporte comme avant.

- [ ] **Step 1: Write the failing test**

Dans `pockettts/pockettts_test.go` :

```go
// A cancelled context stops the generation rather than running the text to its
// end. The test needs the weights: cancellation is about the generation loop,
// and there is no loop without a model.
func TestSynthesizeStopsOnCancel(t *testing.T) {
	engine, voice := testEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	settings := DefaultSettings(engine.lang)
	settings.Seed = 1
	settings.Frame = func([]float32) { cancel() } // stop at the very first frame
	settings.Ctx = ctx

	_, err := engine.Synthesize("Une phrase assez longue pour occuper le modèle un moment.", voice, &settings)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Without a context, nothing changes.
func TestSynthesizeWithoutContext(t *testing.T) {
	engine, voice := testEngine(t)

	settings := DefaultSettings(engine.lang)
	settings.Seed = 1
	sound, err := engine.Synthesize("Bonjour.", voice, &settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(sound) == 0 {
		t.Fatal("no sound produced")
	}
}
```

`testEngine` est l'assistant à écrire s'il n'existe pas déjà dans le fichier — vérifier d'abord avec `grep -n "func testEngine" pockettts/*_test.go` et réutiliser l'existant s'il y en a un. Sinon :

```go
// testEngine opens the French engine on the weights in the Hugging Face cache,
// and loads the first voice it finds. It skips when the model is not there:
// a fresh clone has no weights, and these tests are about the engine's control
// flow, not about anything a fixture could stand in for.
func testEngine(t *testing.T) (*Engine, *Voice) {
	t.Helper()
	lang, err := LookupLanguage(DefaultLanguage)
	if err != nil {
		t.Fatal(err)
	}
	weights := Locate(lang.WeightsPath())
	tokenizer := Locate(lang.TokenizerPath())
	if weights == "" || tokenizer == "" {
		t.Skip("Pocket TTS weights not in the Hugging Face cache")
	}
	engine, err := Open(Options{Weights: weights, Tokenizer: tokenizer, Language: lang.Name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })

	path := Locate(lang.EmbeddingPath("alba"))
	if path == "" {
		t.Skip("no voice in the Hugging Face cache")
	}
	voice, err := engine.LoadVoice(path)
	if err != nil {
		t.Fatal(err)
	}
	return engine, voice
}
```

`Locate` et `EmbeddingPath` arrivent en tâche 3 : écrire cette tâche-ci **après** la 3, ou écrire l'assistant avec les chemins en dur puis le simplifier. L'ordre recommandé est donc 1, 3, 2 ; le plan les numérote dans l'ordre du spec.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/ricardo/dev/golem && go test ./pockettts -run TestSynthesizeStopsOnCancel -v
```

Attendu : échec de compilation, `unknown field Ctx in struct literal`.

- [ ] **Step 3: Write minimal implementation**

Ajouter `"context"` aux imports de `pockettts/pockettts.go`, et le champ à `Settings` :

```go
	// Ctx, when set, stops the generation as soon as it is cancelled: at the
	// next frame within a segment, and between two segments. Synthesize then
	// returns the context's error and whatever sound it had produced is
	// discarded — the caller that cancels has already had the frames, through
	// Frame, and has its own reason to stop.
	Ctx context.Context
```

Dans `Synthesize`, entre deux segments :

```go
	var sound []float32
	for _, segment := range segments {
		if set.Ctx != nil && set.Ctx.Err() != nil {
			return nil, set.Ctx.Err()
		}
		samples, err := m.synthesizeSegment(segment, voice, &set, rng)
		if err != nil {
			return nil, err
		}
		sound = append(sound, samples...)
	}
```

Dans `synthesizeSegment`, la boucle des frames. La difficulté est de ne pas laisser les goroutines de décodage bloquées : `latents` doit être fermé quoi qu'il arrive, et `done` lu. On sort donc de la boucle plutôt que de rendre la main au milieu :

```go
	endFrame := -1
	cancelled := false
	for frame := 0; frame < maxFrames; frame++ {
		if r.Ctx != nil && r.Ctx.Err() != nil {
			cancelled = true
			break
		}
		cond := m.trans.AdvanceLatent(latent, state)
		...
	}
	close(latents)
	sound := <-done
	if cancelled {
		return nil, r.Ctx.Err()
	}
	return sound, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /home/ricardo/dev/golem && go test ./pockettts -run 'TestSynthesize' -v
```

Attendu : PASS, ou SKIP si les poids ne sont pas là. Sur cette machine ils y sont, donc PASS est attendu.

- [ ] **Step 5: Vérifier qu'aucune goroutine ne fuit**

```bash
cd /home/ricardo/dev/golem && go test ./pockettts -run 'TestSynthesize' -race -count=2
```

Attendu : PASS, sans blocage. Un blocage ici signifie que `latents` n'a pas été fermé sur le chemin d'annulation.

- [ ] **Step 6: Commit**

```bash
cd /home/ricardo/dev/golem
git add pockettts/pockettts.go pockettts/pockettts_test.go
git commit -m "A context stops a synthesis in flight

The caller that cancels wants the sound to stop now, not at the end of
the text."
```

---

## Tâche 3 : golem — trouver les voix du cache Hugging Face

**Files:**
- Create: `pockettts/locate.go`
- Modify: `pockettts/languages.go`
- Modify: `cmd/pocket-tts/main.go`
- Test: `pockettts/languages_test.go`

**Interfaces:**
- Consumes: rien
- Produces:
  - `func (l Language) EmbeddingPath(voice string) string` — chemin relatif d'une voix dans un instantané, `"languages/<lang>/embeddings/<voice>.safetensors"`
  - `func Locate(relative string) string` — chemin absolu du premier fichier trouvé sous les instantanés Pocket TTS du cache Hugging Face, `""` si aucun
  - `func LocateVoices(l Language) []string` — noms des voix disponibles pour cette langue, triés, dédoublonnés

- [ ] **Step 1: Write the failing test**

Dans `pockettts/languages_test.go` :

```go
func TestEmbeddingPath(t *testing.T) {
	lang, err := LookupLanguage("french_24l")
	if err != nil {
		t.Fatal(err)
	}
	got := lang.EmbeddingPath("alba")
	want := "languages/french_24l/embeddings/alba.safetensors"
	if got != want {
		t.Errorf("EmbeddingPath = %q, want %q", got, want)
	}
}

// Locate answers with a path or with nothing; either is a valid answer on a
// machine that may or may not have downloaded the model.
func TestLocate(t *testing.T) {
	lang, _ := LookupLanguage("french_24l")
	if p := Locate(lang.WeightsPath()); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Locate returned %q, which does not exist: %v", p, err)
		}
	}
	if p := Locate("languages/french_24l/embeddings/no-such-voice.safetensors"); p != "" {
		t.Errorf("Locate found %q for a voice that does not exist", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/ricardo/dev/golem && go test ./pockettts -run 'TestEmbeddingPath|TestLocate' -v
```

Attendu : échec de compilation, `undefined: Locate`.

- [ ] **Step 3: Write minimal implementation**

`pockettts/languages.go`, à la suite de `TokenizerPath` :

```go
// EmbeddingPath is where a predefined voice sits inside a Pocket TTS snapshot.
// The file is the voice state the model starts from, already computed by
// Kyutai; nothing here encodes a voice from sound.
func (l Language) EmbeddingPath(voice string) string {
	return "languages/" + l.Name + "/embeddings/" + voice + ".safetensors"
}
```

Créer `pockettts/locate.go` :

```go
package pockettts

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The two Hugging Face repositories Kyutai publishes. The weights come from the
// voice-cloning one, the tokenizer and the predefined voices from the other;
// that is the split the upstream configs use. Locate looks in both rather than
// insisting on which, because a machine may have fetched either.
var repositories = []string{
	".cache/huggingface/hub/models--kyutai--pocket-tts/snapshots/*/",
	".cache/huggingface/hub/models--kyutai--pocket-tts-without-voice-cloning/snapshots/*/",
}

// Locate returns the absolute path of relative inside the Hugging Face cache,
// or the empty string when it is not there. relative is what WeightsPath,
// TokenizerPath or EmbeddingPath returned.
//
// A missing file is not an error here: the caller knows what it was looking for
// and can say so better than this function can.
func Locate(relative string) string {
	for _, repo := range repositories {
		found, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), repo, relative))
		for _, p := range found {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// LocateVoices lists the predefined voices available for a language, by name
// and in order. An empty result means the model was never downloaded, or was
// downloaded without its voices.
func LocateVoices(l Language) []string {
	seen := map[string]bool{}
	for _, repo := range repositories {
		pattern := filepath.Join(os.Getenv("HOME"), repo, l.EmbeddingPath("*"))
		found, _ := filepath.Glob(pattern)
		for _, p := range found {
			seen[strings.TrimSuffix(filepath.Base(p), ".safetensors")] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /home/ricardo/dev/golem && go test ./pockettts -run 'TestEmbeddingPath|TestLocate' -v
```

Attendu : PASS. Vérifier à la main que la découverte trouve bien les fichiers du cache :

```bash
ls ~/.cache/huggingface/hub/models--kyutai--pocket-tts*/snapshots/*/languages/french_24l/
```

Attendu : `model.safetensors` et un répertoire `embeddings/`, aux chemins que `Locate` reconstruit.

- [ ] **Step 5: Faire passer `cmd/pocket-tts` par `Locate`**

Dans `cmd/pocket-tts/main.go`, supprimer `orDefault`, `cloningRepo` et `plainRepo`, et remplacer les deux résolutions par :

```go
	if *weights == "" {
		if *weights = orEnv("POCKET_TTS_WEIGHTS", pockettts.Locate(lang.WeightsPath())); *weights == "" {
			fail(missing(lang, "weights", "-weights", "POCKET_TTS_WEIGHTS"))
		}
	}
	if *tokenizer == "" {
		if *tokenizer = orEnv("POCKET_TTS_TOKENIZER", pockettts.Locate(lang.TokenizerPath())); *tokenizer == "" {
			fail(missing(lang, "tokenizer", "-tokenizer", "POCKET_TTS_TOKENIZER"))
		}
	}
```

Retirer les imports devenus inutiles (`path/filepath` s'il ne sert plus qu'à `filepath.Base` dans `flag.Usage` — vérifier avant de le retirer).

- [ ] **Step 6: Vérifier que la commande marche encore**

```bash
cd /home/ricardo/dev/golem && go build ./... && go vet ./... && go test ./...
POCKET_TTS_VOICE=/home/ricardo/dev/gladyss/voix/gladyss.safetensors \
  go run ./cmd/pocket-tts -o /tmp/locate-check.wav "Vérification après le déplacement."
```

Attendu : la ligne « loading … — N s of sound in … » sur stderr, et un `/tmp/locate-check.wav` non vide.

- [ ] **Step 7: Commit**

```bash
cd /home/ricardo/dev/golem
git add pockettts/locate.go pockettts/languages.go pockettts/languages_test.go cmd/pocket-tts/main.go
git commit -m "The package finds its own weights and voices

The command knew where Kyutai's cache is; that knowledge belongs beside
the paths it builds, where a second consumer can reach it."
```

---

## Tâche 4 : golem — README et v0.2.0

**Files:**
- Modify: `README.md`, `pockettts/README.md`

**Interfaces:**
- Consumes: tâches 1 à 3
- Produits: le tag `v0.2.0`, que gladyss requiert en tâche 5

- [ ] **Step 1: Mettre les README à jour**

Dans `pockettts/README.md`, ajouter à la description de l'API les trois nouveautés : `DefaultSettings`, `Settings.Ctx`, `Locate`/`LocateVoices`/`EmbeddingPath`. Chercher les endroits qui décrivent les réglages :

```bash
cd /home/ricardo/dev/golem && grep -n "Settings\|zero value\|Hugging Face" README.md pockettts/README.md
```

Corriger toute phrase qui dit que le zéro d'un réglage vaut le défaut : ce n'est plus vrai.

- [ ] **Step 2: Vérifier tout le dépôt**

```bash
cd /home/ricardo/dev/golem && go build ./... && go vet ./... && go test ./...
```

Attendu : tout vert.

- [ ] **Step 3: Fusionner et taguer**

```bash
cd /home/ricardo/dev/golem
git add README.md pockettts/README.md
git commit -m "The READMEs say what v0.2.0 changed"
git checkout main
git merge --no-ff settings-without-hidden-defaults -m "Merge: an API a second consumer can use"
go build ./... && go test ./...
git tag -a v0.2.0 -m "v0.2.0: explicit settings, cancellation, cache lookup"
git push origin main --follow-tags
```

Attendu : `git tag` liste `v0.1.0` et `v0.2.0`.

---

## Tâche 5 : gladyss — résoudre un nom de voix

**Files:**
- Create: `/home/ricardo/dev/gladyss/voices.go`
- Test: `/home/ricardo/dev/gladyss/voices_test.go`
- Modify: `/home/ricardo/dev/gladyss/go.mod`

**Interfaces:**
- Consumes: `pockettts.Locate`, `pockettts.LocateVoices`, `Language.EmbeddingPath` (tâche 3)
- Produces:
  - `type voiceCatalog struct { dir string; lang pockettts.Language }`
  - `func newVoiceCatalog(dir string, lang pockettts.Language) *voiceCatalog`
  - `func (c *voiceCatalog) resolve(name string) (string, error)` — chemin du `.safetensors` de la voix
  - `func (c *voiceCatalog) names() []string` — union triée des voix locales et du catalogue Kyutai

- [ ] **Step 1: Déclarer la dépendance**

```bash
cd /home/ricardo/dev/gladyss && go get github.com/ThiraSoft/golem@v0.2.0 && cat go.mod
```

Attendu : une ligne `require github.com/ThiraSoft/golem v0.2.0`.

- [ ] **Step 2: Write the failing test**

Créer `/home/ricardo/dev/gladyss/voices_test.go` :

```go
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

// Le catalogue mêle les voix locales et celles de Kyutai, sans doublon et
// dans l'ordre. Les secondes peuvent manquer sur une machine sans modèle : le
// test ne vérifie que les locales et l'ordre.
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
	}
	for i := 1; i < len(noms); i++ {
		if noms[i-1] == noms[i] {
			t.Fatalf("names = %v, attendu sans doublon", noms)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
cd /home/ricardo/dev/gladyss && go test -run 'TestResolve|TestNames' -v
```

Attendu : échec de compilation, `undefined: voiceCatalog`.

- [ ] **Step 4: Write minimal implementation**

Créer `/home/ricardo/dev/gladyss/voices.go` :

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ThiraSoft/golem/pockettts"
)

// voiceCatalog résout un nom de voix vers le fichier d'état que le moteur sait
// charger. Deux sources : le répertoire voix/ du dépôt, et le catalogue publié
// par Kyutai dans le cache Hugging Face. Les voix locales gagnent, pour qu'un
// fichier posé ici puisse recouvrir une voix du catalogue.
type voiceCatalog struct {
	dir  string
	lang pockettts.Language
}

func newVoiceCatalog(dir string, lang pockettts.Language) *voiceCatalog {
	return &voiceCatalog{dir: dir, lang: lang}
}

// resolve rend le chemin du .safetensors de la voix.
func (c *voiceCatalog) resolve(name string) (string, error) {
	local := filepath.Join(c.dir, name+".safetensors")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}

	if p := pockettts.Locate(c.lang.EmbeddingPath(name)); p != "" {
		return p, nil
	}

	// Un WAV seul est une voix à cloner, et le clonage demande l'encodeur Mimi,
	// que golem n'a pas encore. Le dire vaut mieux que « voix inconnue » : le
	// fichier est bien là, c'est l'outil qui manque.
	wav := filepath.Join(c.dir, name+".wav")
	if _, err := os.Stat(wav); err == nil {
		return "", fmt.Errorf("voice %q has only %s: cloning from sound is not implemented yet, "+
			"provide %s instead", name, wav, local)
	}

	return "", fmt.Errorf("unknown voice %q: not in %s, and not in the Pocket TTS catalog for %s",
		name, c.dir, c.lang.Name)
}

// names rend l'union des deux sources, triée et sans doublon. Le service s'en
// sert pour valider une voix demandée et pour répondre à GET /voices.
func (c *voiceCatalog) names() []string {
	seen := map[string]bool{}
	for _, n := range pockettts.LocateVoices(c.lang) {
		seen[n] = true
	}
	found, _ := filepath.Glob(filepath.Join(c.dir, "*.safetensors"))
	for _, p := range found {
		seen[strings.TrimSuffix(filepath.Base(p), ".safetensors")] = true
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

Ajouter `"strings"` aux imports.

- [ ] **Step 5: Run test to verify it passes**

```bash
cd /home/ricardo/dev/gladyss && go test -run 'TestResolve|TestNames' -v
```

Attendu : PASS sur les quatre.

- [ ] **Step 6: Commit**

```bash
cd /home/ricardo/dev/gladyss
git add go.mod go.sum voices.go voices_test.go
git commit -m "Le catalogue des voix, sans daemon pour le dire

voix/ d'abord, puis le catalogue Kyutai du cache Hugging Face."
```

---

## Tâche 6 : gladyss — `engine.go` sur `pockettts`

**Files:**
- Rewrite: `/home/ricardo/dev/gladyss/engine.go`
- Delete: `/home/ricardo/dev/gladyss/protocol_test.go`
- Rewrite: `/home/ricardo/dev/gladyss/engine_test.go`

**Interfaces:**
- Consumes: `voiceCatalog` (tâche 5), `pockettts.Open`, `pockettts.DefaultSettings`, `Settings.Ctx`, `Settings.Frame`, `pockettts.Locate` (tâches 1 à 3)
- Produces: `func NewPocketTTS(voicesDir, voice, player, converter string, speed, pitch, eosThreshold float64) (*PocketTTS, error)` — la signature perd `python` et `script`, gagne `voicesDir`. `Speak`, `Synthesize`, `SynthesizeTo`, `SampleRate`, `Voices`, `Close` gardent exactement les leurs.

- [ ] **Step 1: Write the failing test**

Réécrire `/home/ricardo/dev/gladyss/engine_test.go`. Supprimer tout ce qui monte un faux daemon, garder cette structure :

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThiraSoft/golem/pockettts"
)

// moteurDeTest ouvre le vrai moteur sur les poids du cache Hugging Face, avec
// la voix du dépôt. Il saute si le modèle n'est pas là : un clone nu doit
// pouvoir lancer go test.
func moteurDeTest(t *testing.T) *PocketTTS {
	t.Helper()
	lang, err := pockettts.LookupLanguage("french_24l")
	if err != nil {
		t.Fatal(err)
	}
	if pockettts.Locate(lang.WeightsPath()) == "" {
		t.Skip("poids Pocket TTS absents du cache Hugging Face")
	}
	if _, err := os.Stat(filepath.Join("voix", "gladyss.safetensors")); err != nil {
		t.Skip("voix/gladyss.safetensors absent")
	}
	p, err := NewPocketTTS("voix", "gladyss", "ffplay", "ffmpeg", 1.0, 1.0, 0.0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestSampleRateAvantToutEnonce(t *testing.T) {
	p := moteurDeTest(t)
	if p.SampleRate() != 24000 {
		t.Errorf("SampleRate = %d, attendu 24000", p.SampleRate())
	}
}

func TestSynthesizeRendDuPCM(t *testing.T) {
	p := moteurDeTest(t)
	audio, rate, err := p.Synthesize(context.Background(), Utterance{Text: "Bonjour."})
	if err != nil {
		t.Fatal(err)
	}
	if rate != 24000 {
		t.Errorf("rate = %d, attendu 24000", rate)
	}
	if len(audio) < 2 {
		t.Fatalf("audio de %d octets, attendu du son", len(audio))
	}
	if len(audio)%2 != 0 {
		t.Errorf("audio de %d octets, attendu un nombre pair (s16le)", len(audio))
	}
	if bytes.Equal(audio, make([]byte, len(audio))) {
		t.Error("audio entièrement silencieux")
	}
}

// Un contexte annulé arrête la génération, et ce n'est pas une erreur du point
// de vue du service : c'est ce que fait --stop.
func TestSynthesizeAnnule(t *testing.T) {
	p := moteurDeTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := p.Synthesize(ctx, Utterance{Text: "Une phrase que personne n'entendra jamais."})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, attendu context.Canceled", err)
	}
}

// Une voix inconnue est rejetée avec son nom, pas avec « aucun audio produit ».
func TestSynthesizeVoixInconnue(t *testing.T) {
	p := moteurDeTest(t)
	_, _, err := p.Synthesize(context.Background(), Utterance{Text: "Bonjour.", Voice: "personne"})
	if err == nil {
		t.Fatal("attendu une erreur pour une voix inconnue")
	}
	if !strings.Contains(err.Error(), "personne") {
		t.Errorf("erreur = %q, attendu qu'elle nomme la voix", err)
	}
}

func TestVoicesContientLaVoixLocale(t *testing.T) {
	p := moteurDeTest(t)
	for _, n := range p.Voices() {
		if n == "gladyss" {
			return
		}
	}
	t.Errorf("Voices = %v, attendu qu'il contienne gladyss", p.Voices())
}
```

Ajouter `"strings"` aux imports.

```bash
cd /home/ricardo/dev/gladyss && git rm protocol_test.go
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /home/ricardo/dev/gladyss && go test -run 'TestSynthesize|TestSampleRate|TestVoices' -v
```

Attendu : échec de compilation — `NewPocketTTS` a encore l'ancienne signature.

- [ ] **Step 3: Réécrire la structure et le constructeur**

Dans `engine.go`, supprimer le type `message`, `readMessage`, `pumpAudio`, `send`, `watchCancellation`, et les imports devenus inutiles (`bufio`, `encoding/json`, `os/exec` reste pour ffplay/ffmpeg, `strconv` reste pour `playerArgs`). Nouvelle structure :

```go
// PocketTTS synthétise dans ce processus, par le moteur Go de golem, et pousse
// l'audio produit vers un lecteur. Il n'y a plus ni tube ni daemon : les frames
// arrivent par un callback, au fil de la génération.
type PocketTTS struct {
	engine   *pockettts.Engine
	catalog  *voiceCatalog
	settings pockettts.Settings

	sampleRate   int
	defaultVoice string
	defaultSpeed float64
	defaultPitch float64
	voices       []string
	player       string
	converter    string

	mu     sync.Mutex // un énoncé à la fois : l'état du modèle est unique
	loaded map[string]*pockettts.Voice // voix déjà chargées, protégé par mu
	// rate est le débit de génération observé au dernier énoncé accéléré, en ×
	// temps réel. Protégé par mu.
	rate float64
}
```

Le constructeur :

```go
// NewPocketTTS charge le modèle et la voix par défaut. voicesDir est le
// répertoire des voix locales ; player joue l'audio sur les haut-parleurs,
// converter applique la même chaîne de filtres hors lecture, pour la synthèse
// rendue au client HTTP. eosThreshold règle la détection de fin de parole du
// modèle (cf. main.go).
func NewPocketTTS(voicesDir, voice, player, converter string, speed, pitch, eosThreshold float64) (*PocketTTS, error) {
	lang, err := pockettts.LookupLanguage(pockettts.DefaultLanguage)
	if err != nil {
		return nil, err
	}
	weights := pockettts.Locate(lang.WeightsPath())
	if weights == "" {
		return nil, fmt.Errorf("no Pocket TTS weights for %s in the Hugging Face cache", lang.Name)
	}
	tokenizer := pockettts.Locate(lang.TokenizerPath())
	if tokenizer == "" {
		return nil, fmt.Errorf("no Pocket TTS tokenizer for %s in the Hugging Face cache", lang.Name)
	}

	engine, err := pockettts.Open(pockettts.Options{
		Weights: weights, Tokenizer: tokenizer, Language: lang.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("loading the model: %w", err)
	}

	// Le seuil est posé explicitement, y compris à zéro : c'est une vraie
	// valeur, réglée après mesure, et non un champ laissé vide.
	settings := pockettts.DefaultSettings(lang)
	settings.EndThreshold = eosThreshold

	catalog := newVoiceCatalog(voicesDir, lang)
	p := &PocketTTS{
		engine:       engine,
		catalog:      catalog,
		settings:     settings,
		sampleRate:   pockettts.SampleRate,
		defaultVoice: voice,
		defaultSpeed: speed,
		defaultPitch: pitch,
		voices:       catalog.names(),
		player:       player,
		converter:    converter,
		loaded:       map[string]*pockettts.Voice{},
	}

	// La voix par défaut est chargée maintenant plutôt qu'au premier énoncé :
	// c'est la seule préparation qui coûte, et un nom faux doit se voir au
	// démarrage, pas à la première phrase.
	if _, err := p.voice(voice); err != nil {
		engine.Close()
		return nil, err
	}

	log.Printf("engine ready — %s, %d Hz, %d voices, %s by default",
		lang.Name, p.sampleRate, len(p.voices), voice)
	return p, nil
}

// voice charge une voix, ou rend celle déjà en mémoire. L'appelant tient mu,
// sauf au démarrage où personne d'autre ne touche encore la structure.
func (p *PocketTTS) voice(name string) (*pockettts.Voice, error) {
	if v, ok := p.loaded[name]; ok {
		return v, nil
	}
	path, err := p.catalog.resolve(name)
	if err != nil {
		return nil, err
	}
	v, err := p.engine.LoadVoice(path)
	if err != nil {
		return nil, fmt.Errorf("loading voice %q: %w", name, err)
	}
	p.loaded[name] = v
	return v, nil
}
```

Ajouter les imports `"github.com/ThiraSoft/golem/pockettts"` et retirer ceux qui ne servent plus. `Close` devient :

```go
// Close libère la projection mémoire des poids.
func (p *PocketTTS) Close() error { return p.engine.Close() }
```

- [ ] **Step 4: La génération, partagée par les deux chemins**

Ajouter à `engine.go` la conversion et la boucle commune :

```go
// generate synthétise l'énoncé et écrit le PCM dans out au fil de la
// génération. C'est le seul endroit qui parle au moteur ; Speak et
// SynthesizeTo ne diffèrent que par la destination et par ce qu'ils en font.
//
// L'écriture au fil de l'eau est ce qui permet de commencer à jouer avant la
// fin de la génération : le moteur rend une frame de 80 ms à la fois, il n'y a
// aucune raison de les retenir.
func (p *PocketTTS) generate(ctx context.Context, e Utterance, out io.Writer) error {
	name := e.Voice
	if name == "" {
		name = p.defaultVoice
	}
	v, err := p.voice(name)
	if err != nil {
		return err
	}

	settings := p.settings
	settings.Ctx = ctx
	// Une erreur d'écriture n'arrête pas la génération par elle-même : le
	// lecteur tué par une annulation est le cas normal, et le contexte le dit
	// déjà. On jette les frames suivantes plutôt que de gonfler un tube mort.
	broken := false
	settings.Frame = func(samples []float32) {
		if broken {
			return
		}
		if _, err := out.Write(pcm(samples)); err != nil {
			broken = true
		}
	}

	_, err = p.engine.Synthesize(e.Text, v, &settings)
	return err
}

// pcm convertit des échantillons de [-1, 1] en PCM signé 16 bits little-endian,
// le format que ffplay et l'en-tête WAV attendent. Les valeurs hors bornes sont
// écrêtées : le modèle en produit rarement, et un débordement s'entendrait bien
// plus qu'un écrêtage.
func pcm(samples []float32) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		v := s * 32767
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}
```

Ajouter `"encoding/binary"` aux imports.

- [ ] **Step 5: Réécrire `Speak`**

```go
// Speak synthétise le texte et le joue jusqu'au bout, sauf annulation du contexte.
func (p *PocketTTS) Speak(ctx context.Context, e Utterance) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	speed := e.Speed
	if speed == 0 {
		speed = p.defaultSpeed
	}
	pitch := e.Pitch
	if pitch == 0 {
		pitch = p.defaultPitch
	}

	player := exec.Command(p.player, playerArgs(p.sampleRate, speed, pitch, e.Effects)...)
	playerStdin, err := player.StdinPipe()
	if err != nil {
		return err
	}
	player.Stderr = nil
	if err := player.Start(); err != nil {
		return fmt.Errorf("starting audio player %q: %w", p.player, err)
	}

	// À l'annulation, le son doit cesser immédiatement : la génération s'arrête
	// d'elle-même par le contexte, mais le lecteur a déjà de l'audio en réserve.
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			if player.Process != nil {
				_ = player.Process.Kill()
			}
		case <-stopWatch:
		}
	}()

	// À vitesse normale ou ralentie, le lecteur ne peut pas dépasser le moteur :
	// on lui donne l'audio dès qu'il arrive, c'est le chemin le plus court vers
	// le premier son. Au-delà de 1×, il consommerait plus vite qu'on ne produit
	// et s'affamerait en cours d'énoncé ; pacedWriter mesure le débit réel et ne
	// retient que l'avance strictement nécessaire.
	var genErr error
	if speed <= 1.0 {
		genErr = p.generate(ctx, e, playerStdin)
	} else {
		pacer := newPacedWriter(playerStdin, p.sampleRate, speed, e.Text, p.rate)
		genErr = p.generate(ctx, e, pacer)
		if genErr == nil {
			_ = pacer.Flush()
		}
		if pacer.rate > 0 {
			p.rate = pacer.rate // sert de prior au prochain énoncé
		}
	}

	close(stopWatch)
	<-watchDone
	_ = playerStdin.Close()
	_ = player.Wait() // attend la fin de la lecture : garantit la séquentialité

	return genErr
}
```

`generate` rendant déjà `ctx.Err()` par le moteur, la ligne `return ctx.Err()` de l'ancienne version disparaît.

- [ ] **Step 6: Réécrire `SynthesizeTo`**

`Synthesize` ne change pas d'une ligne. `SynthesizeTo` garde son enveloppe ffmpeg et remplace `p.send` + `p.pumpAudio` par `p.generate` :

```go
func (p *PocketTTS) SynthesizeTo(ctx context.Context, e Utterance, out io.Writer) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	speed := e.Speed
	if speed == 0 {
		speed = p.defaultSpeed
	}
	pitch := e.Pitch
	if pitch == 0 {
		pitch = p.defaultPitch
	}

	dest := out
	var converter *exec.Cmd
	var converterStdin io.WriteCloser

	// Sans filtre à appliquer, le PCM du moteur est déjà celui qu'on veut :
	// inutile de payer un processus de plus.
	if filter := audioFilters(p.sampleRate, speed, pitch, e.Effects); filter != "" {
		converter = exec.Command(p.converter, converterArgs(p.sampleRate, filter)...)
		var err error
		if converterStdin, err = converter.StdinPipe(); err != nil {
			return p.sampleRate, err
		}
		converter.Stdout = out
		converter.Stderr = os.Stderr
		if err := converter.Start(); err != nil {
			return p.sampleRate, fmt.Errorf("starting converter %q: %w", p.converter, err)
		}
		dest = converterStdin
	}

	genErr := p.generate(ctx, e, dest)

	if converter != nil {
		_ = converterStdin.Close()
		convErr := converter.Wait()
		if genErr == nil && convErr != nil {
			return p.sampleRate, fmt.Errorf("audio conversion: %w", convErr)
		}
	}
	return p.sampleRate, genErr
}
```

- [ ] **Step 7: Run tests to verify they pass**

```bash
cd /home/ricardo/dev/gladyss && go build ./... && go vet ./... && go test ./... -v
```

Attendu : compilation propre, tous les tests verts. Si `main.go` ne compile plus, c'est normal : il appelle encore `NewPocketTTS` avec l'ancienne signature — c'est la tâche 7. Faire la tâche 7 avant de relancer, ou corriger l'appel maintenant et laisser le nettoyage des flags à la tâche 7.

- [ ] **Step 8: Course et fuite**

```bash
cd /home/ricardo/dev/gladyss && go test ./... -race -count=2
```

Attendu : PASS, sans avertissement du détecteur de course. `broken` est écrit et lu depuis la seule goroutine du callback, `mu` couvre tout le reste.

- [ ] **Step 9: Commit**

```bash
cd /home/ricardo/dev/gladyss
git add -A engine.go engine_test.go protocol_test.go
git commit -m "Le moteur tourne dans ce processus

Plus de tube, plus de protocole, plus de daemon : les frames arrivent par
un callback et l'annulation passe par le contexte de l'énoncé."
```

---

## Tâche 7 : gladyss — `main.go` sans Python

**Files:**
- Modify: `/home/ricardo/dev/gladyss/main.go` (champs de config lignes 42-43, valeurs par défaut lignes 143-150, flags lignes 162-163, résolution lignes 175-177, fabrique lignes 187-192)

**Interfaces:**
- Consumes: `NewPocketTTS(voicesDir, voice, player, converter string, speed, pitch, eosThreshold float64)` (tâche 6)
- Produces: rien pour les tâches suivantes

- [ ] **Step 1: Retirer les champs de configuration**

Supprimer de la structure de configuration :

```go
	Python       string   `json:"python"`
	Daemon       string   `json:"daemon"`
```

- [ ] **Step 2: Retirer les valeurs par défaut et les flags**

Supprimer `defPython`, `defDaemon` et leurs blocs `if cfg.… != ""`, ainsi que :

```go
	python := flag.String("python", defPython, "daemon's Python interpreter")
	script := flag.String("daemon", defDaemon, "synthesis daemon script")
```

et, plus bas :

```go
	pythonPath := resolve(root, *python)
	scriptPath := resolve(root, *script)
```

Garder `root := binaryDir()` : il sert maintenant à trouver `voix/`.

- [ ] **Step 3: Câbler le répertoire des voix**

Remplacer la fabrique :

```go
	// Le modèle n'est chargé qu'au premier énoncé — le service reste léger tant
	// que personne ne parle. Le chargement est une projection mémoire : il coûte
	// désormais des millisecondes, là où le daemon Python coûtait des secondes.
	voicesDir := filepath.Join(root, "voix")
	engine := NewLazyEngine(func() (*PocketTTS, error) {
		return NewPocketTTS(voicesDir, *voice, *player, *converter,
			*speed, *pitch, *eosThreshold)
	}, *idleTimeout)
```

Vérifier que `path/filepath` est importé ; l'ajouter sinon. Si `resolve` n'a plus d'appelant, le supprimer :

```bash
cd /home/ricardo/dev/gladyss && grep -n 'resolve' main.go
```

- [ ] **Step 4: Vérifier la compilation et les tests**

```bash
cd /home/ricardo/dev/gladyss && go build ./... && go vet ./... && go test ./...
```

Attendu : tout vert, aucune occurrence restante :

```bash
cd /home/ricardo/dev/gladyss && grep -rn "python\|Python\|daemon\|Daemon" --include=*.go .
```

Attendu : aucune ligne, hors éventuel commentaire historique à supprimer aussi.

- [ ] **Step 5: Commit**

```bash
cd /home/ricardo/dev/gladyss
git add main.go
git commit -m "Le service n'a plus d'interpréteur à trouver

-python et -daemon partent avec le processus qu'ils désignaient."
```

---

## Tâche 8 : gladyss — supprimer Python du dépôt

**Files:**
- Delete: `tts_daemon.py`, `requirements.txt`
- Modify: `install.sh`, `README.md`

**Interfaces:**
- Consumes: tâches 6 et 7
- Produces: rien

- [ ] **Step 1: Supprimer les fichiers**

```bash
cd /home/ricardo/dev/gladyss && git rm tts_daemon.py requirements.txt
```

- [ ] **Step 2: Élaguer `install.sh`**

Supprimer le bloc de création de l'environnement Python (`VENV`, `uv venv`, `uv pip install`, le repli `python3 -m venv` / `ensurepip` / `pip install`, et la variable `TORCH_INDEX`), et retirer `python3` de la liste des outils vérifiés ainsi que des quatre lignes d'installation par distribution :

```bash
cd /home/ricardo/dev/gladyss && grep -n "python\|venv\|uv \|torch\|VENV\|TORCH" install.sh
```

Traiter chaque ligne trouvée. Le drapeau `--cuda`, qui ne sert qu'à choisir la roue torch, part aussi : chercher ses mentions dans l'aide du script.

Il reste : la vérification des outils (`go ffmpeg ffplay curl`), `go build -o gladyss .`, l'installation du client et de son alias, la vérification finale parlée.

- [ ] **Step 3: Vérifier que l'installation marche**

```bash
cd /home/ricardo/dev/gladyss && bash -n install.sh && ./install.sh --no-cli --no-check
```

Attendu : le script passe l'analyse syntaxique, construit le binaire, et ne mentionne ni venv ni torch.

- [ ] **Step 4: Reprendre le README**

```bash
cd /home/ricardo/dev/gladyss && grep -n "venv\|uv pip\|torch\|CUDA\|Python\|python\|requirements\|Go 1\|1 Go\|4,8 Go" README.md
```

Traiter chaque occurrence :

- la section « Démarrage » : `./install.sh` puis `./gladyss`, sans la partie « À la main » qui décrit `uv venv`, l'index CPU de PyTorch et `uv pip install`. La remplacer par `go build -o gladyss . && ./gladyss`.
- les phrases sur le poids de l'environnement (« ~1 Go, contre 4,8 Go avec la roue CUDA ») : supprimées, il n'y a plus d'environnement.
- la phrase sur macOS et la roue PyPI : supprimée.
- « La synthèse tourne entièrement en local via Kyutai Pocket TTS » : garder, en ajoutant que le moteur est celui de golem, en Go, sans Python ni cgo.
- `ffplay` et `ffmpeg` restent décrits comme requis : c'est encore vrai.

Ajouter une note sur ce que la migration retire : le clonage d'une voix depuis un WAV n'est pas encore disponible, un `.safetensors` est attendu dans `voix/`.

- [ ] **Step 5: Vérifier qu'il ne reste rien**

```bash
cd /home/ricardo/dev/gladyss && ls *.py requirements.txt 2>&1 | tail -1 && grep -rn "venv\|torch" --include=* . | grep -v '^./.git' | grep -v docs/superpowers
```

Attendu : « No such file or directory », et aucune occurrence hors des specs et plans.

- [ ] **Step 6: Commit**

```bash
cd /home/ricardo/dev/gladyss
git add -A install.sh README.md
git commit -m "Python quitte le dépôt

Le daemon, ses dépendances, sa venv d'un gigaoctet et les sections du
README qui l'expliquaient."
```

---

## Tâche 9 : vérification de bout en bout

**Files:** aucun — c'est l'écoute

**Interfaces:**
- Consumes: tâches 1 à 8

- [ ] **Step 1: Construire et lancer**

```bash
cd /home/ricardo/dev/gladyss && go build -o gladyss . && ./gladyss &
sleep 2
```

Attendu dans le journal : `listening on http://127.0.0.1:8420`.

- [ ] **Step 2: Le seuil de fin de parole**

C'est la régression la plus probable : `EndThreshold` valait 0.0 dans le daemon Python, et un zéro mal interprété rendrait -4.

```bash
curl -s -X POST 127.0.0.1:8420/v1/audio/speech -H 'Content-Type: application/json' \
  -d '{"input":"Mais je sais pas"}' -o /tmp/court.wav && ls -l /tmp/court.wav
```

Attendu : un WAV d'environ une seconde de parole — à 24 kHz et 16 bits mono, environ 48 000 octets de données, donc un fichier d'à peu près 50 ko. Un fichier de l'ordre de 10 ko signifie que le seuil vaut -4 : vérifier alors que `NewPocketTTS` pose bien `settings.EndThreshold = eosThreshold` **après** `DefaultSettings`.

- [ ] **Step 3: La parole sur les haut-parleurs**

```bash
curl -s -X POST 127.0.0.1:8420/say -H 'Content-Type: application/json' \
  -d '{"text":"Bonjour, la migration est terminée."}'
```

Attendu : la phrase se fait entendre, de la voix de gladyss, sans hachure ni silence au début.

- [ ] **Step 4: L'annulation**

```bash
curl -s -X POST 127.0.0.1:8420/say -H 'Content-Type: application/json' \
  -d '{"text":"Voici une phrase suffisamment longue pour que je puisse la couper au milieu sans difficulté."}' &
sleep 1.5
curl -s -X POST 127.0.0.1:8420/stop
curl -s -X POST 127.0.0.1:8420/say -H 'Content-Type: application/json' \
  -d '{"text":"Et celle-ci doit passer entière."}'
```

Attendu : la première phrase s'arrête net à la coupure, la seconde s'entend en entier. Une seconde phrase tronquée signifie qu'une annulation a fui sur l'énoncé suivant.

- [ ] **Step 5: Vitesse, hauteur et effet**

```bash
curl -s -X POST 127.0.0.1:8420/say -H 'Content-Type: application/json' \
  -d '{"text":"Plus vite et plus haut.","speed":1.6,"pitch":1.2,"effects":[{"name":"echo"}]}'
```

Attendu : la phrase est accélérée, plus aiguë, avec l'écho — et ne s'interrompt pas en cours de route, ce qui vérifierait que `pacedWriter` est toujours correctement alimenté.

- [ ] **Step 6: Le catalogue**

```bash
curl -s 127.0.0.1:8420/voices
```

Attendu : un JSON contenant `gladyss` et les voix du catalogue Kyutai.

- [ ] **Step 7: Arrêter et clore**

```bash
kill %1
cd /home/ricardo/dev/gladyss && go build ./... && go vet ./... && go test ./...
git status --short
```

Attendu : tout vert, arbre propre.

- [ ] **Step 8: Consigner les mesures**

Relever dans le journal du service le temps de chargement et le débit de génération (`× temps réel`), et les ajouter au README à la place des chiffres du daemon Python s'il y en avait. Commiter :

```bash
cd /home/ricardo/dev/gladyss
git add README.md
git commit -m "Les chiffres de la nouvelle chaîne"
```

---

## Tâche 10 : fusion

- [ ] **Step 1: Relire le diff en entier**

```bash
cd /home/ricardo/dev/gladyss && git diff main...inference-in-process --stat && git diff main...inference-in-process
```

Attendu : `engine.go` réécrit, `voices.go` ajouté, `main.go` allégé, les fichiers Python supprimés, et **aucune modification** de `controller.go`, `http.go`, `pacing.go`, `lazy.go`, `speed.go`, `effects.go`, `text.go`, `wav.go`.

- [ ] **Step 2: Fusionner**

```bash
cd /home/ricardo/dev/gladyss
git checkout main
git merge --no-ff inference-in-process -m "gladyss parle sans Python"
go build ./... && go test ./...
```

Attendu : tout vert sur `main`.
