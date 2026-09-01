#!/usr/bin/env bash
# Installe gladyss : binaire Go et client en ligne de commande. Idempotent — le
# relancer après un `git pull` reconstruit ce qu'il faut.
set -euo pipefail

RACINE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
INSTALLER_CLI=1
VERIFIER=1
TELECHARGER=1
SANS_QUESTION=0

# Le dépôt public de Kyutai : les poids du catalogue, sans le clonage de voix.
# Celui qui porte le clonage est sous conditions, et c'est à l'utilisateur de
# les accepter — cf. « Les voix » dans le README.
DEPOT=kyutai/pocket-tts-without-voice-cloning
LANGUE=french_24l
CACHE="$HOME/.cache/huggingface/hub/models--kyutai--pocket-tts-without-voice-cloning"
ARBRE="https://huggingface.co/api/models/$DEPOT/tree/main"

usage() {
  cat <<'USAGE'
Usage: ./install.sh [options]

  --no-cli     N'installe pas le client `gladyss` dans le PATH
  --no-check   Saute la vérification finale (qui charge le modèle et parle)
  --no-model   Ne télécharge pas les poids absents du cache Hugging Face
  --yes        Ne demande pas confirmation avant de télécharger les poids
  --bin-dir D  Où installer le client (défaut: ~/.local/bin)
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-cli)   INSTALLER_CLI=0; shift ;;
    --no-check) VERIFIER=0; shift ;;
    --no-model) TELECHARGER=0; shift ;;
    --yes|-y)   SANS_QUESTION=1; shift ;;
    --bin-dir)  BIN_DIR="$2"; shift 2 ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "Option inconnue : $1" >&2; usage; exit 1 ;;
  esac
done

etape() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
echec() { printf '\033[31merreur:\033[0m %s\n' "$1" >&2; exit 1; }

# deja_la dit si un fichier du dépôt est déjà dans le cache, en cherchant où le
# service cherchera : les deux dépôts de Kyutai, toutes révisions confondues.
deja_la() {
  compgen -G "$HOME/.cache/huggingface/hub/models--kyutai--pocket-tts*/snapshots/*/$1" >/dev/null
}

poids_presents() { deja_la "languages/$LANGUE/model.safetensors"; }

# lister rend, pour un répertoire du dépôt, une ligne « chemin taille » par
# fichier. Le modèle et les voix se téléchargent séparément et un cache peut
# porter l'un sans les autres : c'est fichier par fichier qu'il faut comparer,
# pas en concluant du modèle au catalogue.
lister() {
  curl -sSf "$ARBRE/$1" 2>/dev/null | tr '{' '\n' | sed -n \
    's/.*"size"[[:space:]]*:[[:space:]]*\([0-9]*\).*"path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\2 \1/p'
}

# mo arrondit des octets en mébioctets, pour l'affichage seulement.
mo() { echo $(( ($1 + 524288) / 1048576 )); }

# recuperer télécharge un fichier du dépôt vers le cache, en passant par un
# fichier temporaire : une coupure laisse un .part, jamais un fichier tronqué
# que le prochain passage prendrait pour complet.
recuperer() {
  local chemin="$1" cible="$CACHE/snapshots/$sha/$1"
  shift
  [ -s "$cible" ] && return 0
  curl -fL --retry 3 "$@" -o "$cible.part" \
    "https://huggingface.co/$DEPOT/resolve/$sha/$chemin" \
    || { rm -f "$cible.part"; echec "échec du téléchargement de $chemin"; }
  mv "$cible.part" "$cible"
}

case "$(uname -s)" in
  Linux)  SYSTEME=linux ;;
  Darwin) SYSTEME=macos ;;
  *) echec "système non géré : $(uname -s). Linux et macOS seulement." ;;
esac

# --- Dépendances système ------------------------------------------------------
# ffplay lit l'audio sur les haut-parleurs, ffmpeg applique les filtres hors
# lecture. Les deux viennent du même paquet, on vérifie les deux quand même :
# certaines distributions livrent ffmpeg sans ffplay.
etape "Vérification des dépendances ($SYSTEME)"
manquants=()
for outil in go ffmpeg ffplay curl; do
  command -v "$outil" >/dev/null 2>&1 || manquants+=("$outil")
done

if [ ${#manquants[@]} -gt 0 ]; then
  echo "Manquants : ${manquants[*]}"
  if [ "$SYSTEME" = macos ]; then
    echo "  brew install go ffmpeg curl"
  elif command -v pacman >/dev/null 2>&1; then
    echo "  sudo pacman -S go ffmpeg curl"
  elif command -v apt-get >/dev/null 2>&1; then
    echo "  sudo apt-get install golang ffmpeg curl"
  elif command -v dnf >/dev/null 2>&1; then
    echo "  sudo dnf install golang ffmpeg curl"
  fi
  echec "installe ces outils puis relance ./install.sh"
fi
echo "$(go version | cut -d' ' -f3), ffmpeg présent"

# --- Binaire ------------------------------------------------------------------
# Build de production : -s -w jettent la table des symboles et les données de
# débogage, -trimpath retire les chemins absolus de la machine de compilation.
# Un tiers de binaire en moins, et rien qui manque à l'exécution — un profilage
# ou un débogueur, eux, veulent un `go build` nu.
etape "Construction du binaire"
(cd "$RACINE" && go build -trimpath -ldflags="-s -w" -o gladyss .)
echo "gladyss construit ($(du -h "$RACINE/gladyss" | cut -f1))"

# --- Client en ligne de commande ----------------------------------------------
if [ "$INSTALLER_CLI" -eq 1 ]; then
  etape "Client en ligne de commande"
  mkdir -p "$BIN_DIR"
  install -m 755 "$RACINE/cli/gladyss" "$BIN_DIR/gladyss"
  echo "installé dans $BIN_DIR/gladyss"
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) echo "attention : $BIN_DIR n'est pas dans ton PATH" ;;
  esac
fi

# --- Poids du modèle ----------------------------------------------------------
# Le service ne télécharge rien de lui-même : il lit le cache Hugging Face, là
# où l'outillage de Kyutai dépose les poids. On les y met donc ici, par curl. Le
# dépôt sans clonage est public — pas de jeton, pas de conditions à accepter —
# et le client `hf` ramènerait le Python dont ce projet se passe.
if [ "$TELECHARGER" -eq 1 ]; then
  etape "Poids du modèle ($LANGUE)"

  # Ce que le dépôt publie : le modèle et son tokenizer d'un côté, les voix du
  # catalogue de l'autre.
  publie=$(lister "languages/$LANGUE" | grep -E '\.(safetensors|model) '; lister "languages/$LANGUE/embeddings")

  if [ -z "$publie" ]; then
    # Hors ligne, un cache déjà pourvu reste utilisable : on le dit sans faire
    # échouer l'installation.
    if poids_presents; then
      echo "Hugging Face injoignable — le cache en place est gardé tel quel"
    else
      echec "Hugging Face injoignable : impossible de lire le contenu de $DEPOT"
    fi
  else
    total=0
    manquants=()
    connus=0
    while read -r chemin taille; do
      [ -n "$chemin" ] || continue
      connus=$((connus + 1))
      if ! deja_la "$chemin"; then
        manquants+=("$chemin")
        total=$((total + taille))
      fi
    done <<< "$publie"

    if [ ${#manquants[@]} -eq 0 ]; then
      echo "cache complet : $connus fichiers, rien à télécharger"
    else
      echo "${#manquants[@]} fichiers manquants sur $connus ($(mo $total) Mo)"
      if [ "$SANS_QUESTION" -eq 0 ] && [ -t 0 ]; then
        printf 'Les télécharger depuis %s (CC-BY-4.0) ? [O/n] ' "$DEPOT"
        read -r reponse
        case "$reponse" in [nN]*) TELECHARGER=0 ;; esac
      fi

      if [ "$TELECHARGER" -eq 1 ]; then
        sha=$(curl -sSf "https://huggingface.co/api/models/$DEPOT" \
          | tr ',' '\n' | sed -n 's/.*"sha"[[:space:]]*:[[:space:]]*"\([0-9a-f]\{40\}\)".*/\1/p' | head -1)
        [ -n "$sha" ] || echec "impossible de lire la révision de $DEPOT"
        mkdir -p "$CACHE/snapshots/$sha/languages/$LANGUE/embeddings"

        for chemin in "${manquants[@]}"; do
          # Une barre pour ce qui se fait attendre, une ligne muette pour le
          # reste : vingt-six barres à la suite ne renseignent personne.
          case "$chemin" in
            *model.safetensors) echo "  $chemin"; recuperer "$chemin" --progress-bar ;;
            *)                  recuperer "$chemin" -s ;;
          esac
        done
        echo "  ${#manquants[@]} fichiers récupérés"

        # refs/main est ce que le cache Hugging Face garde pour savoir quelle
        # révision il tient : l'écrire évite qu'un `hf download` ultérieur
        # reparte de zéro.
        mkdir -p "$CACHE/refs"
        printf '%s' "$sha" > "$CACHE/refs/main"
        echo "  cache à jour dans $CACHE"
      fi
    fi
  fi

  # Sans poids, le service démarre mais se tait : la vérification échouerait sur
  # un défaut qu'on connaît déjà.
  if ! poids_presents; then
    VERIFIER=0
    echo "poids absents — le service tournera mais ne pourra pas parler"
  fi
fi

# --- Vérification -------------------------------------------------------------
# Le premier énoncé charge le modèle : compter une trentaine de secondes. Les
# poids, eux, sont déjà là — l'étape précédente s'en est assurée.
if [ "$VERIFIER" -eq 1 ]; then
  etape "Vérification (le modèle se charge, puis la machine parle)"
  "$RACINE/gladyss" > "$RACINE/.install-check.log" 2>&1 &
  SERVICE_PID=$!
  trap 'kill "$SERVICE_PID" 2>/dev/null || true' EXIT

  for _ in $(seq 30); do
    curl -s --max-time 1 http://127.0.0.1:8420/health >/dev/null 2>&1 && break
    sleep 1
  done
  curl -s --max-time 2 http://127.0.0.1:8420/health >/dev/null 2>&1 \
    || { cat "$RACINE/.install-check.log"; echec "le service n'a pas répondu"; }
  echo "service en ligne"

  curl -s -X POST http://127.0.0.1:8420/say \
    -H 'Content-Type: application/json' \
    -d '{"text":"Installation terminee, la synthese vocale fonctionne."}' >/dev/null
  echo "énoncé envoyé — tu devrais l'entendre d'ici quelques secondes"

  # /voices ne répond qu'une fois le moteur réveillé par l'énoncé ci-dessus.
  compte=""
  for _ in $(seq 120); do
    # Compte les entrées du tableau "voices" sans quitter le shell : le service
    # n'a plus d'interpréteur Python sous la main pour lire son propre JSON.
    compte=$(curl -s --max-time 2 http://127.0.0.1:8420/voices \
      | sed -n 's/.*"voices"[[:space:]]*:[[:space:]]*\[\([^]]*\)\].*/\1/p' \
      | tr ',' '\n' | grep -c '"' 2>/dev/null || true)
    [ "$compte" = "0" ] && compte=""
    [ -n "$compte" ] && break
    sleep 1
  done
  if [ -n "$compte" ]; then
    echo "moteur prêt, $compte voix au catalogue"
  else
    cat "$RACINE/.install-check.log"
    echec "le moteur n'a pas fini de démarrer — voir la sortie ci-dessus"
  fi

  kill "$SERVICE_PID" 2>/dev/null || true
  trap - EXIT
  rm -f "$RACINE/.install-check.log"
fi

etape "Terminé"
cat <<EOF
Lancer le service :   $RACINE/gladyss
Parler :              gladyss "Bonjour"
Options du service :  $RACINE/gladyss -h

Les voix disponibles sont celles de voix/ et celles du catalogue Kyutai, dans le
cache HuggingFace. Cloner une voix depuis un enregistrement demande l'autre jeu
de poids, sous conditions : cf. « Les voix » dans le README.
EOF
