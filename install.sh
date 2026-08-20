#!/usr/bin/env bash
# Installe gladyss : binaire Go et client en ligne de commande. Idempotent — le
# relancer après un `git pull` reconstruit ce qu'il faut.
set -euo pipefail

RACINE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
INSTALLER_CLI=1
VERIFIER=1

usage() {
  cat <<'USAGE'
Usage: ./install.sh [options]

  --no-cli     N'installe pas le client `gladyss` (et `say`) dans le PATH
  --no-check   Saute la vérification finale (qui charge le modèle et parle)
  --bin-dir D  Où installer le client (défaut: ~/.local/bin)
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-cli)   INSTALLER_CLI=0; shift ;;
    --no-check) VERIFIER=0; shift ;;
    --bin-dir)  BIN_DIR="$2"; shift 2 ;;
    -h|--help)  usage; exit 0 ;;
    *) echo "Option inconnue : $1" >&2; usage; exit 1 ;;
  esac
done

etape() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
echec() { printf '\033[31merreur:\033[0m %s\n' "$1" >&2; exit 1; }

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
etape "Construction du binaire"
(cd "$RACINE" && go build -o gladyss .)
ln -sf "$RACINE/gladyss" "$RACINE/say"
echo "gladyss construit ($(du -h "$RACINE/gladyss" | cut -f1))"

# --- Client en ligne de commande ----------------------------------------------
if [ "$INSTALLER_CLI" -eq 1 ]; then
  etape "Client en ligne de commande"
  mkdir -p "$BIN_DIR"
  install -m 755 "$RACINE/cli/gladyss" "$BIN_DIR/gladyss"
  install -m 755 "$RACINE/cli/say" "$BIN_DIR/say"
  echo "installé dans $BIN_DIR/gladyss (et alias $BIN_DIR/say)"
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) echo "attention : $BIN_DIR n'est pas dans ton PATH" ;;
  esac
fi

# --- Vérification -------------------------------------------------------------
# Le premier énoncé charge le modèle : compter une trentaine de secondes, et
# davantage si les poids doivent encore être téléchargés.
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
Parler :              gladyss "Bonjour" (ou say "Bonjour")
Options du service :  $RACINE/gladyss -h

Les voix disponibles sont celles de voix/ et celles déjà téléchargées dans le
cache HuggingFace. Cloner une voix depuis un enregistrement n'est pas encore
possible sans Python : cf. « Les voix » dans le README.
EOF
