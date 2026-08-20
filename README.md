# gladyss — service de synthèse vocale française, 100 % local

Un serveur HTTP sans authentification qui lit à voix haute, sur les haut-parleurs
de la machine, le texte qu'on lui envoie. Les demandes sont mises en file et lues
une par une, dans l'ordre d'arrivée.

La synthèse tourne entièrement en local via **Kyutai Pocket TTS**, dont
l'inférence est faite en Go pur par le paquet `pockettts` de
[golem](https://github.com/ThiraSoft/golem) : aucun texte ne sort de la machine,
aucune clé d'API, aucun quota, et aucun Python.

## Démarrage

```bash
./install.sh
./gladyss
```

`install.sh` vérifie les dépendances système, construit le binaire, installe le
client `gladyss` (et son alias `say`) dans `~/.local/bin`, puis prononce une
phrase pour vérifier que la chaîne audio marche de bout en bout. Il est
idempotent : on le relance après un `git pull`. `--no-cli` saute l'installation
du client, `--no-check` la vérification finale, `--bin-dir` change la
destination du client.

À la main, si tu préfères :

```bash
go build -o gladyss . && ./gladyss
```

C'est tout : l'inférence est en Go, par le paquet `pockettts` de
[golem](https://github.com/ThiraSoft/golem). Pas de Python, pas de PyTorch, pas
d'environnement virtuel — le binaire et les poids suffisent.

Le service écoute sur `127.0.0.1:8420`. Les poids du modèle (~1 Go) sont
attendus dans le cache HuggingFace, là où l'outillage de Kyutai les dépose ;
gladyss les projette en mémoire, ce qui prend quelques millisecondes.

`ffplay` et `ffmpeg` doivent être installés (`brew install ffmpeg` sur macOS,
le paquet `ffmpeg` de ta distribution sur Linux) : le premier joue le son, le
second applique les filtres pour l'audio rendu par `/v1/audio/speech`.

### Le client en ligne de commande

`cli/gladyss` est un client du service, distinct du binaire. Une fois installé par
`install.sh` :

```bash
gladyss "Bonjour"
echo "depuis un pipeline" | gladyss
gladyss -v alba -s 1.3 "Plus vite, autre voix"
gladyss --stop
```

*(L'ancien alias de commande `say` reste également disponible).*

`gladyss --help` liste les options, `gladyss --voices` le catalogue. Le service doit
tourner : le client ne le démarre pas, il le dit s'il ne répond pas.

Chaque option a une variable d'environnement équivalente, pour fixer ses
préférences une fois pour toutes plutôt que de les répéter à chaque appel :

| Variable | Option | Défaut |
|---|---|---|
| `GLADYSS_URL` (ou `SAY_URL`) | — | `http://127.0.0.1:8420` |
| `GLADYSS_SPEED` (ou `SAY_SPEED`) | `-s`, `--speed` | `1.0` |
| `GLADYSS_VOICE` (ou `SAY_VOICE`) | `-v`, `--voice` | celle du service |
| `GLADYSS_PITCH` (ou `SAY_PITCH`) | `-p`, `--pitch` | celle du service |
| `GLADYSS_FX` (ou `SAY_FX`) | `--fx` | aucun effet |
| `GLADYSS_FORCE` (ou `SAY_FORCE`) | `--force` | intensité nominale |

L'option l'emporte sur la variable, qui l'emporte sur le défaut. Une élocution
un peu plus vive, par exemple, tient dans une ligne du fichier de profil :

```bash
export GLADYSS_SPEED=1.1
```

Au-delà de `1.0`, la lecture passe par un filtre de tempo : le lecteur consomme
alors l'audio plus vite qu'il n'arrive, et le service doit prendre un peu
d'avance avant de commencer — cf. `pacing.go` dans « Architecture ». Le premier
énoncé qui suit un démarrage paie l'observation du débit, environ 340 ms ; les
suivants réutilisent la mesure et partent sans attendre. À `1.0`, la question ne
se pose pas : le flux part dès le premier morceau.

## Routes

| Route | Effet |
|---|---|
| `POST /say` | Met le texte en file. Répond `202` avec sa position d'attente. |
| `POST /v1/audio/speech` | Synthétise et **renvoie l'audio** au client, sans le jouer. Compatible OpenAI. |
| `POST /skip` | Interrompt l'énoncé en cours et enchaîne sur le suivant. |
| `POST /stop` | Interrompt l'énoncé en cours **et vide la file**. |
| `GET /queue` | État courant : énoncé lu et énoncés en attente. |
| `GET /voices` | Catalogue des voix et voix par défaut. |
| `GET /effects` | Catalogue des effets sonores. |
| `GET /health` | Sonde de vie. |

`/say` accepte le texte sous trois formes, au choix :

```bash
curl -X POST localhost:8420/say -d "Bonjour, comment ça va ?"
curl -X POST localhost:8420/say -H 'Content-Type: application/json' -d '{"text":"Bonjour"}'
curl "localhost:8420/say?text=Bonjour"
```

Attention avec `-d` : curl écrase les retours à la ligne. Pour un fichier ou du
texte multiligne, utilise `--data-binary @fichier.txt`.

La voix se choisit par requête, en paramètre ou dans le corps JSON :

```bash
curl -X POST "localhost:8420/say?voice=jean" -d "Bonjour"
curl -X POST localhost:8420/say -H 'Content-Type: application/json' \
     -d '{"text":"Bonjour","voice":"alba"}'
```

Une voix inconnue est refusée en `400` avec la liste des voix valides, plutôt
que d'échouer plus tard, une fois l'énoncé déjà en file.

**Le texte est nettoyé avant synthèse**, sur `/say` comme sur
`/v1/audio/speech`. La réponse de `/say` renvoie le texte tel qu'il sera
prononcé. Cf. « Ce que le modèle sait lire ».

Le débit (`0.5` à `3.0`) et la hauteur (`0.5` à `2.0`) se règlent de la même façon :

```bash
curl -X POST "localhost:8420/say?speed=1.4" -d "Un peu plus vite"
curl -X POST "localhost:8420/say?voice=jean&pitch=0.8" -d "Plus grave"
```

Les effets sonores s'ajoutent en JSON, avec une force par effet :

```bash
curl -X POST localhost:8420/say -H 'Content-Type: application/json' -d '{
  "text": "Bonjour",
  "effets": [{"effet": "robot", "force": 1.5}, {"effet": "echo", "force": 0.6}]
}'

curl -X POST "localhost:8420/say?text=Bonjour&fx=robot,vibrato"   # raccourci, force nominale
```

Contrôle :

```bash
curl -X POST localhost:8420/skip
curl -X POST localhost:8420/stop
curl localhost:8420/queue
```

### Synthèse sans lecture — `/v1/audio/speech`

`/say` parle sur les haut-parleurs ; cette route-là rend l'audio dans le corps
de la réponse. Elle suit le contrat de l'API `/v1/audio/speech` d'OpenAI (celui
que sert aussi Kokoro-FastAPI), pour qu'un client écrit pour ce format n'ait
qu'à changer son URL de base.

```bash
curl localhost:8420/v1/audio/speech -H 'Content-Type: application/json' \
     -d '{"input":"Bonjour","voice":"mary","speed":1.33}' -o bonjour.wav
```

| Champ | |
|---|---|
| `input` | le texte, obligatoire |
| `voice` | une voix du catalogue ; **inconnue, elle retombe sur la voix par défaut** |
| `speed` | `0.5` à `3.0` |
| `response_format` | `wav` (défaut) ou `pcm` — le moteur ne produit que du PCM |
| `stream` | `true` : l'audio part au fil de la génération (premier octet à ~0,3 s au lieu d'attendre l'énoncé complet) |
| `pitch`, `effets` | extensions maison, mêmes valeurs que `/say` |
| `model`, `lang_code` | acceptés puis ignorés : un seul modèle, une seule langue |

### Streaming

```bash
curl -N localhost:8420/v1/audio/speech -H 'Content-Type: application/json' \
     -d '{"input":"Bonjour","stream":true,"response_format":"pcm"}' \
  | sox -t raw -r 24000 -e signed -b 16 -c 1 - -d
```

Mesuré sur une phrase de ~5 s : premier octet à **0,31 s** en streaming, contre
**2,14 s** sans. Le client peut donc commencer à jouer pendant la génération.

Deux pièges, d'où le choix de `pcm` dans l'exemple :

- **`stream` + `wav` donne un en-tête aux tailles fausses** (`0xFFFFFFFF`) : la
  longueur est inconnue quand les en-têtes partent. Les décodeurs tolérants
  lisent jusqu'à la fin du flux, les stricts refusent. En `pcm`, il n'y a pas
  d'en-tête à mentir : le taux est dans `X-Sample-Rate`.
- **Le client applique une contre-pression.** S'il lit au rythme de la lecture
  audio, la requête reste ouverte toute la durée du son : un timeout HTTP global
  côté client couperait l'audio en plein milieu.

Les requêtes s'empilent : l'état du modèle est unique, donc protégé par un
verrou, et une seconde requête attend son tour au lieu d'interrompre la
première. Une déconnexion client annule le contexte de sa propre synthèse, et
d'elle seule.

La voix réellement utilisée est renvoyée dans `X-Voix-Utilisee`, le taux
d'échantillonnage dans `X-Sample-Rate` : sans quoi un client qui envoie un nom
de voix inconnu n'aurait aucun moyen de s'apercevoir de la substitution.

Le WAV est assemblé par le service, pas par ffmpeg : sur un tube ffmpeg ne peut
pas revenir écrire les tailles réelles et laisse `0xFFFFFFFF`, ce que refusent
les décodeurs stricts — dont `decodeAudioData` des navigateurs.

Cette route ne touche pas à la file de lecture : rien n'apparaît dans `/queue`,
`/skip` et `/stop` ne l'affectent pas. En revanche le modèle n'a qu'un état :
une synthèse demandée pendant une lecture sur les haut-parleurs attend son tour.

**Brancher Nova dessus** — dans `~/.nova/default.json` :

```json
"ttsURL": "http://localhost:8420"
```

Nova ajoute `/v1/audio/speech` lui-même et lit tout le corps de la réponse.
Attention à son client HTTP, dont le timeout est de 10 s : la synthèse tourne à
~2,7× le temps réel, donc un énoncé de plus de ~25 s de parole sera coupé côté
Nova, pas ici.

## Configuration & Options

Le service résout sa configuration dans l'ordre suivant :
1. **Drapeaux CLI** (`-voice`, `-speed`, `-addr`, etc.)
2. **Variables d'environnement** (`GLADYSS_DEFAULT_VOICE`, `GLADYSS_SPEED`, `GLADYSS_ADDR`, etc.)
3. **Fichier de configuration utilisateur** : `~/.config/gladyss/config.json` (ou `~/.config/say/config.json`)
4. **Valeurs par défaut intégrées**

Exemple de fichier `~/.config/gladyss/config.json` :
```json
{
  "voice": "gladyss",
  "speed": 1.1,
  "pitch": 1.0,
  "idle_timeout": "15m"
}
```

### Options du binaire

```
-addr    127.0.0.1:8420    adresse d'écoute
-voice   estelle           voix par défaut (ou définie dans ~/.config/gladyss/config.json)
-speed   1.0               débit de parole par défaut (0.5 à 3.0)
-pitch   1.0               hauteur de voix par défaut (0.5 à 2.0)
-eos-threshold 0.0         seuil de fin de parole du modèle (cf. « Les phrases courtes »)
-idle-timeout 15m          délai d'inactivité avant déchargement du modèle
-player  ffplay            lecteur audio recevant du PCM sur stdin
-converter ffmpeg          applique les filtres hors lecture (/v1/audio/speech)
```

Kyutai publie 26 voix officielles : `alba`, `anna`, `azelma`, `bill_boerst`,
`caro_davy`, `charles`, `cosette`, `eponine`, `estelle` (défaut public), `eve`,
`fantine`, `george`, `giovanni`, `jane`, `javert`, `jean`, `juergen`, `lola`,
`marius`, `mary`, `michael`, `paul`, `peter_yearsley`, `rafael`, `stuart_bell`,
`vera`.

**Celles qui sont disponibles sont celles déjà présentes dans le cache
HuggingFace.** gladyss ne télécharge rien : il lit ce qui est là. C'est la
contrepartie de la disparition de Python, dont la bibliothèque allait chercher
une voix manquante toute seule. Pour en ajouter une, la récupérer depuis
`kyutai/pocket-tts-without-voice-cloning` — elle y est publiée déjà encodée, un
`.safetensors` par voix sous `languages/french_24l/embeddings/` — ou déposer le
fichier directement dans `voix/`.

S'y ajoutent les voix personnalisées de `voix/` (ex : `gladyss`).
`./gladyss -voice jean` change la voix par défaut ; `?voice=` la surcharge
requête par requête. Une voix coûte le temps de lire son fichier à son premier
usage, puis reste en mémoire pour toute la vie du service ; celle passée à
`-voice` est chargée au démarrage, pour qu'un nom faux se voie tout de suite.

Le catalogue (`GET /voices`) n'est pas codé en dur : il est l'union de `voix/`
et de ce que le cache HuggingFace contient.

### Les essayer toutes

```bash
./essai_voix.sh                          # les 26, à la suite
./essai_voix.sh alba jean estelle        # seulement celles-ci
PHRASE="Mon texte à moi" ./essai_voix.sh
ANNONCER=0 ./essai_voix.sh               # sans annoncer le nom avant chaque essai
VITESSE=1.4 ./essai_voix.sh              # en accéléré
```

Le script attend la fin de chaque lecture avant de passer à la suivante, sinon
les 26 partiraient en file d'un coup et on ne saurait plus qui parle. `Ctrl-C`
coupe le son et rend la main.

## Cloner une voix

> **Indisponible pour l'instant.** L'encodeur Mimi, qui transforme un
> enregistrement en état de voix, vivait dans le daemon Python et n'existe pas
> encore en Go. Cette section décrit le mécanisme tel qu'il était et tel qu'il
> reviendra ; les mesures qu'elle contient restent valables. Une voix déjà
> encodée en `.safetensors`, elle, se charge et se joue normalement.

Pocket TTS sait imiter une voix à partir d'un simple extrait audio, sans
entraînement : il encode l'extrait et s'en sert d'amorce pour la génération. Le
service reprend ce mécanisme **sans nouvelle route** — une voix clonée est un
fichier dans `voix/`, et elle apparaît dans `/voices` à côté des voix du
catalogue. `?voice=`, `speed`, `pitch` et les effets marchent à l'identique.

### 1. Obtenir l'accès aux poids

Le clonage vit dans un jeu de poids distinct du catalogue, distribué sous
conditions. Sans lui, le service tourne mais refuse les voix clonées avec un
message qui rappelle la marche à suivre.

1. Accepter les conditions sur <https://huggingface.co/kyutai/pocket-tts>
   (accès automatique, pas de validation manuelle).
2. Se connecter localement : `hf auth login`, ou exporter `HF_TOKEN`.
   Un jeton *fine-grained* doit porter la permission **« Read access to contents
   of all public gated repos you can access »**, sinon le téléchargement
   retourne `403` malgré les conditions acceptées.

Vérification :

```bash
curl -sSI -H "Authorization: Bearer $HF_TOKEN" -o /dev/null -w '%{http_code}\n' -L \
  https://huggingface.co/kyutai/pocket-tts/resolve/main/languages/french_24l/model.safetensors
```

`200`, c'est bon. `403` signifie authentifié mais pas autorisé, et distingue
deux causes : `curl -sS -H "Authorization: Bearer $HF_TOKEN"
https://huggingface.co/api/whoami-v2` montre `canReadGatedRepos` — s'il vaut
`true`, ce sont les conditions du dépôt qui n'ont pas été acceptées par ce
compte, pas le jeton qui est trop étroit.

Les conditions qu'on accepte là interdisent notamment le clonage d'une voix sans
le consentement de la personne. Cloner un personnage de fiction pour un service
qui ne sort pas de la machine ne pose pas de question ; publier l'audio produit,
ou imiter une voix réelle, en pose une.

Le repli est silencieux côté `pocket_tts` : le paquet essaie les poids complets,
et bascule sur `pocket-tts-without-voice-cloning` en cas d'échec. C'est pour ça
que le daemon vérifie `has_voice_cloning` avant de cloner, plutôt que de laisser
partir un message d'erreur qui parlerait de fichier introuvable.

### 2. Fabriquer le prompt

Le modèle reproduit le timbre de l'extrait **et ses défauts** : bruit de fond,
compression, réverbération, tout revient dans la voix générée. D'où un montage
soigné plutôt qu'un extrait pris au hasard.

`preparer_voix.sh` lit une liste de clips et en fait un prompt utilisable :

```bash
./preparer_voix.sh gladyss       # voix/gladyss.liste -> voix/gladyss.wav
```

Il aligne les niveaux RMS clip par clip, rogne les silences de bord, intercale
0,2 s de blanc, rééchantillonne en 24 kHz mono — le format natif du modèle — et
laisse 1 dBFS de marge de crête. Les sources de jeu arrivent normalisées à
0 dBFS : sans cette marge, le rééchantillonnage écrête.

| Réglage | Défaut | |
|---|---|---|
| `BLANC` | `0.2` | silence entre deux clips, en secondes |
| `CIBLE_RMS` | `-13` | niveau commun, en dBFS |

#### Quelle longueur de prompt ?

Pocket TTS ne tronque **pas** le prompt : `truncate` vaut `False` par défaut et
le daemon Python ne le demandait pas, donc un extrait de deux minutes était
encodé en entier. Mais plus long n'est pas mieux — mesuré ici, même texte (~29 s de parole
attendues) à chaque fois :

| Prompt | Audio rendu | Cache | 1er appel |
|---|---|---|---|
| 26 s | 28,2 s | 61 Mio | 11 s |
| 56 s | 31,0 s | 131 Mio | 19 s |
| 67 s | 34,9 s | — | — |
| 84 s | **6 à 8 s** | — | — |
| 128 s | **5 s** | 300 Mio | 33 s |

Deux effets, tous deux défavorables :

- **Au-delà de ~70 s, la génération s'effondre.** Le modèle émet son EOS presque
  tout de suite et rend quelques secondes au lieu de trente, *sans erreur* —
  l'entraînement n'a pas vu de prompts si longs, et les positions sortent du
  domaine appris. C'est le pire cas de figure : un échec silencieux. La rupture a
  été observée entre 67 s (correct) et 84 s (tronqué) ; `preparer_voix.sh` avertit
  au-delà de 70 s.
- **Entre 30 et 70 s, le débit ralentit.** Le même texte passe de 28,2 s à 34,9 s
  de parole, soit 24 % plus lent, pour un cache qui double. Rattrapable avec
  `?speed=`, mais ce n'est pas un gain.

D'où la fourchette retenue : **20 à 30 s**. Cinq secondes suffisent à reconnaître
une voix, la prosodie se tient mieux au-delà de vingt, et rien de mesurable ne
s'améliore ensuite.

### 3. S'en servir

Rien à déclarer : le service balaie `voix/` à chaque démarrage et annonce ce
qu'il y trouve.

```bash
go build -o say . && ./say
curl localhost:8420/voices                        # gladyss y figure
curl -X POST "localhost:8420/say?voice=gladyss" -d "Bonjour, sujet de test."
./say -voice gladyss                              # ou par défaut, pour tout le service
```

Le premier usage encodait le WAV, puis le daemon écrivait l'état du modèle dans
`voix/<nom>.safetensors` : c'est ce fichier que le service relit aujourd'hui.

| Sur la même phrase courte | |
|---|---|
| 1er appel, encodage d'un prompt de 26 s inclus | 3,0 s |
| 1er appel après redémarrage, relu depuis le cache | 0,5 s |
| appels suivants, voix en mémoire | 0,6 s |
| taille du cache | 61,3 Mio par voix |

Soit ~2,4 s d'encodage économisées à chaque démarrage, contre 61 Mio sur le
disque — le cache est déjà couvert par le `.gitignore`, comme les WAV.

Ce cache est un fichier dérivé : `preparer_voix.sh` le supprime quand il
reconstruit le WAV, sans quoi un cache calculé sur l'ancien montage
l'emporterait sur le nouveau, puisque le service préfère le `.safetensors`.

### `gladyss`, clonée sur GLaDOS

`ref/glados/sounds/` contient 1344 répliques de GLaDOS en VF, en 44,1 kHz mono
16 bits, tous mesurés (durée, RMS, crête, facteur de platitude, part de silence).
Sont écartés d'office les répliques en patate, le générique chanté, Portal 1, le
combat final, les glitches et les rires : leur timbre altéré reviendrait tel quel
dans la voix clonée.

Trois montages ont été essayés, puis départagés à l'oreille :

| Liste | Prompt | |
|---|---|---|
| `gladyss` | 5 extraits du registre annonceuse, 27,9 s | **retenue** |
| `gladyss_dense` | 4 extraits à débit dense, 26,0 s | écartée |
| `gladyss_continu` | une seule prise continue, 26,9 s | écartée |

Ce qui a fait la différence n'était pas le nombre d'extraits mais **le taux de
silence interne**. Les deux premières listes exigeaient *moins* de 6 % de silence
— un critère qui semble bon (parole dense, pas de blanc perdu) mais qui
sélectionne les répliques les plus enlevées de GLaDOS, alors que son trait de
caractère est le débit posé. La liste retenue prend l'inverse : 16 à 25 % de
silence interne, dans le registre « annonceuse de coop ». `preparer_voix.sh` ne
rogne que les silences de bord, les pauses arrivent donc intactes au modèle, et le
même texte se dit en 38 s au lieu de 29.

Les deux listes écartées sont conservées **sans leur WAV**, donc hors catalogue :
`./preparer_voix.sh gladyss_dense` les ramène si besoin. Des prompts de 56 s et
67 s ont aussi été montés et écoutés, tous deux battus.

Les critères et les exclusions sont commentés en tête de chaque `.liste`.

Trois conséquences pratiques :

- **`echo` à faible force améliore nettement le rendu**, en ajoutant la chambre
  de test qui manque à une voix synthétisée « à nu ». Il n'est pas appliqué par
  défaut, c'est une décision de goût — à demander par requête :

  ```bash
  curl -X POST localhost:8420/say -H 'Content-Type: application/json' -d '{
    "text": "Le test va commencer.", "speed": 1.15,
    "effets": [{"effet": "echo", "force": 0.45}]
  }'
  ```

  Le tempo s'applique **avant** l'écho : accélérer ne rétrécit pas la chambre.
- **L'effet `robot` devient inutile.** Le vocodeur de GLaDOS est déjà dans le
  prompt, donc dans la voix clonée. L'empiler dessus rend la parole bouillie.
- **La voix reste française.** Le modèle est `french_24l` ; le clonage transporte
  le timbre, pas la langue.

#### La durée d'un même énoncé varie d'un rendu à l'autre

Trois synthèses du même texte, à réglages rigoureusement identiques, ont donné
**34,2 s / 32,8 s / 40,1 s** — 22 % d'écart. La génération échantillonne, la durée
n'est donc pas reproductible. Conséquence pour qui mesure : **comparer deux
réglages sur un seul rendu chacun ne prouve rien sur le débit.** Le timbre et
l'intelligibilité, eux, ne bougent pas. Un débit stable se réglerait sur
`Settings.Temperature`, pas sur `speed`.

## Architecture

```
POST /say ──▶ Controller (file séquentielle) ──▶ PocketTTS ──▶ ffplay
                   │                                 │
                /skip /stop                golem/pockettts
              (annule le contexte)      (dans ce processus)
                                                     │
POST /v1/audio/speech ───────────────────────────────┘──▶ ffmpeg ──▶ WAV
        (rendu au client, hors file)                      (filtres)
```

Autour du service, deux fichiers d'outillage : `install.sh` construit le binaire,
`cli/say` est le client en ligne de commande.

Le service lui-même tient en cinq pièces, chacune testable seule :

- **`controller.go`** — la file d'`Enonce` (texte + voix). Un seul à la fois ;
  `Skip` et `Stop` annulent le contexte de l'énoncé en cours. Ne connaît rien
  à l'audio.
- **`engine.go`** — le moteur. Charge les poids une fois (projection mémoire,
  quelques millisecondes) et synthétise dans ce processus, par
  `golem/pockettts` : les frames de 80 ms arrivent par un callback et partent
  aussitôt, soit dans `ffplay` (lecture), soit dans `ffmpeg` puis dans un
  tampon (`SynthesizeTo`, pour la route compatible OpenAI). Les deux chemins
  partagent la même chaîne de filtres : à réglages égaux, ils sonnent pareil.
- **`voices.go`** — la résolution des noms de voix. `voix/` d'abord, puis le
  catalogue Kyutai du cache HuggingFace.
- **`pacing.go`** — le régulateur de lecture. Le daemon génère à environ 1,2 ×
  le temps réel : au-delà de `speed = 1`, le lecteur consommerait l'audio plus
  vite qu'il n'arrive et se retrouverait à sec en pleine phrase. Le régulateur
  mesure le débit réel sur les 400 premières millisecondes d'audio, en déduit
  l'avance strictement nécessaire (`durée estimée × (1 − débit / vitesse)`,
  bornée à 4 s) et lâche le flux dès qu'elle est constituée — souvent tout de
  suite, puisque 1,2 × couvre les vitesses courantes. Le débit mesuré sert de
  prior à l'énoncé suivant, qui se passe alors de mesure. En pratique : premier
  son à ~0,3 s à `speed 1.1`, contre la durée complète de la génération quand
  l'énoncé était bufférisé en entier.

Le modèle reste chargé pour toute la durée du service, ou jusqu'à
`-idle-timeout` ; c'est ce qui rend la latence acceptable. L'annulation passe
par le contexte de l'énoncé : `Settings.Ctx` arrête la génération à la frame
suivante, et le lecteur est tué dans la foulée pour que le son cesse aussi.
Rien ne peut fuir sur l'énoncé suivant — il n'y a plus de tube partagé où un
`cancel` tardif pouvait atterrir.

### Les effets sonores

| Effet | Rendu | Ce que module la force |
|---|---|---|
| `robot` | voix métallique, monotone | taille de fenêtre FFT : 0,5 → 1024, 2 → 128 |
| `tpain` | quantifié et dédoublé | idem, plus la profondeur du chorus |
| `telephone` | voix de combiné | resserrement de la bande passante |
| `chorus` | voix dédoublée | profondeur du dédoublement |
| `vibrato` | ondulation de hauteur | amplitude de l'ondulation |
| `echo` | écho | retard et intensité |

Force entre `0` et `2`, `0` valant la force nominale (1.0). Les effets
s'appliquent **dans l'ordre du tableau, après** la hauteur et le tempo : l'écho
d'une voix filtrée n'est pas le filtrage d'une voix qui résonne.

Un mot sur l'autotune : `tpain` en reproduit le **timbre**, pas le principe. Un
vrai autotune détecte la hauteur et la corrige vers les notes d'une gamme ;
aucun filtre ffmpeg ne fait ça, il faudrait un plugin LADSPA et un ffmpeg
recompilé. Sur de la parole, c'est de toute façon le timbre qui s'entend.

### Ce que le modèle sait lire

Le modèle tokenise le texte avec un SentencePiece de **4 000 tokens seulement**.
Tout caractère absent de ce vocabulaire est découpé en octets UTF-8 bruts, que le
modèle n'a jamais vus proprement à l'entraînement : il hache la syllabe ou avale
le mot. C'est visible au tokenizer :

| Texte | Tokens |
|---|---|
| `l'entrée` (apostrophe droite) | `▁l` `'` `ent` `r` `é` `e` |
| `l’entrée` (apostrophe typographique) | `▁l` `<0xE2>` `<0x80>` `<0x99>` `ent` `r` `é` `e` |
| `lentrée` (après nettoyage) | `▁l` `ent` `r` `é` `e` |

Le trou est large en français. Sont **hors vocabulaire** : toutes les majuscules
accentuées (`É À Ç Ê Î Ô Ù Û…`), les minuscules `ë î ï ù û ÿ œ æ` — donc « où »,
« sûr », « sœur », « août » —, tous les guillemets `« » “ ”`, les parenthèses et
crochets, les points de suspension `…`, les espaces insécables (omniprésentes en
français), et les symboles `° € £ § × ±`. Sont en vocabulaire, en revanche :
`é è à â ô ç`, les tirets `- – —`, `" % & : ; , . ! ?` et les chiffres.

`nettoyerTexte` (dans `texte.go`) ramène le texte dans ce vocabulaire, au point
de passage commun aux deux routes — `reglages.normaliser` — pour que `/say` et
`/v1/audio/speech` prononcent exactement le même texte :

| Famille | Traitement | Exemple |
|---|---|---|
| Apostrophes, toutes variantes | retirées, mots soudés | `l'entrée` → `lentrée` |
| Majuscules accentuées | minusculisées (la casse ne s'entend pas) | `École` → `école` |
| `ë î ï ù û ÿ œ æ` | translittérées | `sûr` → `sur`, `sœur` → `soeur` |
| Guillemets, parenthèses, puces | retirés sans souder les mots | `km/h` → `km h` |
| `…` | `...` | |
| Espaces insécables | espace ordinaire | |
| Symboles à lecture univoque | dits en mots | `20 °C` → `20 degrés Celsius` |

Rien de ce qui s'entend n'est perdu : les caractères retirés ne se prononcent
pas, les autres sont translittérés vers une graphie de même prononciation. Seul
`ï` est un compromis assumé — « naïf » → « naif » perd le tréma, mais un tréma en
octets bruts coûtait le mot entier.

Les retours à la ligne, eux, sont préservés : le moteur s'en sert pour découper
les phrases, les écraser changerait la prosodie.

### Les phrases courtes et le seuil de fin de parole

Le modèle décide seul quand il a fini de parler : à chaque frame, il émet un logit
de fin de séquence, et la génération s'arrête dès qu'il dépasse `eos_threshold`
(puis continue `frames_after_eos` frames, 8 pour le français, soit 0,64 s — c'est
le petit silence qui termine un énoncé normal).

Le défaut de la bibliothèque est **-4,0**, ce qui déclenche la fin dès ~2 % de
probabilité. Sur une phrase courte, le modèle s'arrête alors en pleine syllabe.
Mesuré sur « Mais je sais pas », 10 rendus par réglage, parole utile :

| Seuil | Parole utile (10 rendus) | Tronqués |
|---|---|---|
| `-4.0` (défaut bibliothèque) | 0,08 → 0,72 s | **8/10** |
| `0.0` (défaut du service) | 0,96 → 1,52 s | 0/10 |

Le service passe donc **0,0**. Au-delà, le modèle ne trouve plus sa fin : à `+2.0`,
un « Bonjour, comment vas-tu aujourd'hui ? » a duré 6 s, la génération atteignant
sa longueur maximale sans jamais conclure. `-eos-threshold` reste réglable si
besoin.

La bibliothèque prévoyait un autre remède — rallonger les entrées courtes avec des
espaces (`pad_with_spaces_for_short_inputs`, « the model does not perform well
when there are very few tokens ») — mais il est inopérant : l'option est
désactivée pour le français, et `split_into_best_sentences` passe de toute façon
le texte au `strip()`, qui retire le rembourrage.

**Limite résiduelle** : un énoncé d'**un seul mot** (« Oui. ») reste fragile, ~1
rendu sur 6 sortant trop court. Monter le seuil le corrige mais fait divaguer les
phrases normales : le compromis se paie d'un côté ou de l'autre, et on préfère
protéger le cas courant.

### Le débit de parole

Pocket TTS n'expose **aucun réglage de vitesse** : le débit dépend du modèle et
de la voix. L'accélération se fait donc à la lecture, via le filtre `atempo` de
ffmpeg, qui change le tempo sans toucher à la hauteur — la voix ne devient pas
plus aiguë.

ffmpeg plafonne chaque `atempo` à 2× ; au-delà, le service cascade deux filtres
(3.0 devient `atempo=2,atempo=1.5`).

Gain réel mesuré sur une même phrase, de bout en bout :

| Vitesse | 1.0 | 1.3 | 1.6 | 2.0 | 2.5 | 3.0 |
|---|---|---|---|---|---|---|
| Durée | 5,4 s | 4,3 s | 4,0 s | 3,2 s | 2,8 s | 2,7 s |

Le gain se tasse au-delà de 2× : la lecture rattrape alors la synthèse et
l'attend. Le plancher, c'est la vitesse de génération (RTF 0,37), pas le tempo.

## Mesures

Le modèle est `french_24l`, 24 kHz mono, dans les deux cas. Les colonnes disent
surtout l'écart entre un accélérateur intégré et un CPU seul : la synthèse est
le poste dominant, tout le reste en découle.

Les deux premières colonnes datent du daemon Python ; la troisième est la même
machine que la deuxième, après la migration vers `golem/pockettts`.

| | M3, PyTorch | i7-9700K, PyTorch | i7-9700K, golem |
|---|---|---|---|
| Vitesse de synthèse | 2,7× le temps réel | 1,19× | **2,15 à 2,26×** |
| Réveil du moteur (modèle en cache disque) | ~4 s | ~19 s, dont ~15 s d'import de torch | **~50 ms** (projection mémoire) |
| Premier échantillon audio | ~200 ms après la requête | ~440 ms | ~210 ms côté moteur¹ |
| Encodage d'un prompt de clonage de ~27 s | ~2,4 s | ~7 s | indisponible (cf. « Cloner une voix ») |
| Réaction à `/skip` et `/stop` | son coupé immédiatement, ~30 ms côté moteur | idem | idem, ~80 ms (une frame) |
| Longueur de prompt exploitable | jusqu'à ~70 s ; au-delà, énoncés tronqués | idem | idem |

¹ Mesuré dans golem, pas à travers le service. À travers `/v1/audio/speech`, le
premier son arrive plutôt vers 2 s, et **ce n'est pas ffmpeg** : la mesure donne
1 987 ms avec la chaîne de filtres active (`speed 1.1`) contre 2 097 ms sans
aucun filtre (`speed 1.0`). L'écart entre les 210 ms du moteur et ces 2 s reste
à expliquer.

La vitesse de synthèse est mesurée sur trois rendus de la même phrase de six
secondes, moteur déjà chaud :

```bash
curl -s -X POST 127.0.0.1:8420/v1/audio/speech -H 'Content-Type: application/json' \
  -d '{"input":"Le chat dort sur le canapé du salon, tranquillement, pendant que la pluie tombe dehors sans discontinuer."}' -o /tmp/m.wav
```

Le débit de synthèse est ce qui décide de la latence à la lecture : dès qu'on
demande une vitesse supérieure à lui, le lecteur consommerait l'audio plus vite
qu'il n'arrive. C'est le régulateur de `pacing.go` qui arbitre, et il mesure ce
débit à l'exécution plutôt que de le supposer — un `speed 1.1` passe en flux
direct sur M3, et désormais aussi sur le i7, dont le débit est passé de 1,19× à
2,2×.

## Limites connues

- **Une seule instance de lecture.** C'est voulu : le service parle sur des
  haut-parleurs, pas dans un flux réseau. `/v1/audio/speech`, elle, rend bien
  l'audio au client — mais partage l'état du modèle, donc sa requête attend la
  fin de l'énoncé en cours de lecture.
- **Un WAV streamé annonce des tailles fausses.** Contrainte du format, pas du
  service : demander `pcm` pour un flux propre.
- **Licence du modèle** : code MIT, poids CC-BY-4.0 — usage commercial autorisé
  avec attribution à Kyutai.
- **Cloner une voix depuis un enregistrement n'est plus possible.** L'encodeur
  Mimi, qui transforme un WAV en état de voix, n'existe pas encore en Go : il
  vivait dans le daemon Python, parti avec lui. Une voix déjà encodée en
  `.safetensors` — dont `voix/gladyss.safetensors` — marche exactement comme
  avant ; un WAV seul est refusé avec un message qui le dit. Cf. « Cloner une
  voix », qui décrit la marche à suivre telle qu'elle reviendra.
- **Aucune voix n'est téléchargée automatiquement.** Le service lit le cache
  HuggingFace, il ne le remplit pas. Cf. « Les voix ».
- **Pas d'authentification, pas de limite de débit.** Écoute sur la boucle locale
  uniquement. Ne pas exposer tel quel sur un réseau.
- **Un énoncé d'un seul mot peut sortir tronqué**, environ un rendu sur six. Le
  modèle conclut trop tôt quand il a très peu de tokens en entrée ; cf. « Les
  phrases courtes et le seuil de fin de parole ».
- **Le français n'existe qu'en variante 24 couches** chez Kyutai, plus lente que
  le modèle anglais (2,7× le temps réel contre 8,3×).

## Tests

```bash
go test -race ./...
```

84 tests couvrent la file (ordre, séquentialité, skip, stop, positions, arrêt),
les routes HTTP (formats d'entrée, voix, vitesse, hauteur, effets, validation),
la route compatible OpenAI (en-tête WAV, PCM brut, streaming, formats refusés,
voix inconnue tolérée, aucun passage par la file), la résolution des noms de
voix (locale, catalogue, WAV seul, inconnue), le calcul des chaînes de filtres
ffmpeg et la régulation de lecture. La plupart utilisent un faux moteur : ils
sont instantanés et ne produisent aucun son.

Cinq d'entre eux chargent le vrai modèle et vérifient ce qu'aucun faux ne peut
dire — que la synthèse rend du PCM non silencieux au bon taux, qu'un contexte
annulé l'interrompt, qu'une voix inconnue est refusée par son nom. Ils sautent
proprement si les poids ne sont pas dans le cache, pour qu'un clone nu reste
vert.
