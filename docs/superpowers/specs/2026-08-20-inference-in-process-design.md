# Inférence en processus : gladyss sans Python

**Date** : 2026-08-20
**Statut** : validé, prêt pour le plan d'implémentation

## Le but

gladyss est déjà écrit en Go, à une exception près : `tts_daemon.py`, qui
charge Kyutai Pocket TTS sous PyTorch et parle au service par un tube et un
protocole JSON. Ce chantier supprime ce processus et fait l'inférence dans le
service lui-même, via le paquet `pockettts` de
[golem](https://github.com/ThiraSoft/golem).

Ce qui disparaît : Python, PyTorch, la venv d'environ un gigaoctet, le
protocole, le tube, et un processus. Ce qui reste, pour l'instant : `ffplay`
pour la lecture et `ffmpeg` pour les filtres.

Ce chantier est le premier de quatre. Les suivants — DSP en Go, sortie audio en
Go, encodeur Mimi dans golem — ont leur propre spec.

### Ce qui est déjà vérifié

`voix/gladyss.safetensors`, écrit par le daemon Python, se charge tel quel avec
`pockettts.Engine.LoadVoice` : `cmd/pocket-tts` l'a synthétisé à ×2,59 temps
réel sur cette machine. Le format d'état de voix est donc commun aux deux
implémentations, et la voix de gladyss n'a pas à être régénérée.

## Partie A — golem v0.2.0

Trois ajouts, tous dans `pockettts`. Ils sont cassants, d'où le v0.2.0 ; golem
n'a qu'un consommateur.

### A1. Annulation

`pockettts.Settings` reçoit un champ `Ctx context.Context`. `Synthesize` le
vérifie à chaque frame et entre deux segments, et rend `ctx.Err()` quand il est
annulé.

C'est ce dont gladyss a besoin pour `--stop` et pour un client HTTP qui
raccroche. La sémantique tombe juste : dans gladyss, une annulation demandée
est une issue normale, pas une erreur, et `ctx.Err()` est exactement ce que le
code appelant teste déjà.

L'alternative — un `Frame` qui rend un booléen — a été écartée : elle ne
distingue pas l'arrêt demandé de l'échec, et le contexte est déjà le vocabulaire
de l'appelant.

### A2. Fin des zéros ambigus

`pockettts.DefaultSettings(lang Language) Settings` rend une structure déjà
remplie, que l'appelant modifie avant de la passer. `Settings.defaults` et sa
règle « zéro veut dire la valeur par défaut » disparaissent.

C'est une correction de bug, pas un confort. `EndThreshold: 0` signifie
aujourd'hui « -4 », or gladyss utilise 0.0 comme *vraie* valeur de seuil, réglée
après mesure : la migration naïve rendrait silencieusement le seuil du modèle au
lieu du sien, et la fin de parole serait coupée. `Temperature` porte le même
piège.

### A3. `Language.EmbeddingPath(voice string) string`

Le chemin d'une voix prédéfinie dans le cache Hugging Face, à côté des
`WeightsPath` et `TokenizerPath` existants. Sans lui, gladyss recopie la
logique de résolution de `cmd/pocket-tts/main.go`.

## Partie B — gladyss : `engine.go`

### La surface publique ne bouge pas

`PocketTTS` garde `Speak`, `Synthesize`, `SynthesizeTo`, `SampleRate`,
`Voices` et `Close`, avec les mêmes signatures. `controller.go`, `http.go`,
`pacing.go` et `lazy.go` ne sont pas touchés : `LazyEngine` prend une fabrique
de `*PocketTTS`, et cette fabrique change seulement d'intérieur.

Le déchargement après inactivité garde son sens, mais change de nature : les
poids sont projetés en mémoire (`mmap`), l'ouverture prend 17 ms au lieu des
secondes du daemon. Le flag `-idle-timeout` reste, sa valeur par défaut est à
reconsidérer une fois la migration mesurée.

### L'intérieur

Les champs `cmd`, `stdin`, `stdout` cèdent la place à :

- `engine *pockettts.Engine`
- `voices map[string]*pockettts.Voice`, un cache des voix chargées
- `settings pockettts.Settings`, les réglages par défaut du service

`mu` reste, et pour la même raison qu'avant : l'état du modèle est unique, un
énoncé à la fois.

`readMessage`, `pumpAudio`, `send`, `watchCancellation` et le type `message`
sont supprimés. À leur place, `Settings.Frame` reçoit les frames de 80 ms
(12,5 par seconde), les convertit en PCM signé 16 bits little-endian et les
écrit vers la destination — l'entrée de `ffplay` pour `Speak`, celle de
`ffmpeg` ou le client pour `SynthesizeTo`. Les deux méthodes partagent cette
conversion.

`watchCancellation` disparaît avec le problème qu'il résolvait : il empêchait
un « cancel » tardif de couper l'énoncé *suivant* sur un tube partagé. Sans
tube, l'annulation est portée par le contexte de l'énoncé et ne peut pas fuir
sur le suivant.

### Résolution des voix

Un nom de voix se résout dans cet ordre :

1. `voix/<nom>.safetensors` — les voix locales, dont `gladyss`
2. `Language.EmbeddingPath(<nom>)` — le catalogue Kyutai dans le cache HF

`Voices()` rend l'union des deux, scannée une fois au démarrage du moteur, et
garde donc son rôle de validation dans `http.go`.

Un `voix/<nom>.wav` sans `.safetensors` à côté rend une erreur qui dit quoi
faire : le clonage depuis un WAV demande l'encodeur Mimi, qui est le quatrième
chantier. C'est la seule fonction que cette migration retire, et elle est
retirée sciemment.

### Les erreurs

Un message `{"type": "error"}` du daemon devient une erreur Go rendue par
`Synthesize`. Le soin pris dans `pumpAudio` à ne pas perdre le message du
daemon — pour qu'une voix inconnue dise laquelle plutôt que « aucun audio
produit » — n'a plus d'objet : l'erreur remonte directement.

## Partie C — ce qui est supprimé

- `tts_daemon.py`, `requirements.txt`
- `protocol_test.go` en entier
- les flags `-python` et `-daemon`, et les constantes qui les servent
- dans `install.sh` : `uv venv`, l'installation de torch, l'index CPU de PyTorch
- dans `README.md` : les sections d'installation Python, les tailles de venv, la
  mention des roues CUDA
- dans `go.mod` : rien à retirer, une ligne `require` à ajouter

`install.sh` se réduit à un `go build`, l'installation du client et la
vérification finale.

## Partie D — les tests

`engine_test.go` est aujourd'hui bâti sur un faux daemon qui parle le protocole.
Le protocole disparaissant, ces tests aussi. À la place :

- **Résolution de voix** : table de cas sur un `voix/` temporaire — voix locale,
  voix du catalogue, WAV seul, nom inconnu. Sans modèle, sans audio.
- **Bout en bout** : un `PocketTTS` réel synthétise une phrase courte et vérifie
  qu'il en sort du PCM non nul au bon taux, puis qu'un contexte annulé
  interrompt la génération. Le test saute si les poids ne sont pas là — c'est
  la convention de golem, et elle garde `go test ./...` vert sur un clone nu.

`controller_test.go`, `http_test.go`, `speed_test.go`, `pacing_test.go`,
`effects_test.go` et `wav_test.go` ne changent pas : ils ne connaissent pas le
daemon.

## Vérification

Le chantier est fini quand, sur cette machine :

1. `go build ./...` et `go test ./...` passent, et aucun `.py` ne subsiste
2. `./gladyss` puis `gladyss "phrase"` sonne comme avant la migration
3. `gladyss --stop` coupe la parole immédiatement, et l'énoncé suivant part
   intact
4. `/v1/audio/speech` rend un WAV identique, aux réglages près, à celui de
   `/say`
5. le seuil de fin de parole vaut bien 0.0 et non -4 : c'est la régression la
   plus probable, et elle s'entend sur une phrase de quatre mots — « Mais je
   sais pas » doit durer environ une seconde, pas deux dixièmes
