#!/usr/bin/env bash
# Installe gladyss : environnement Python du daemon, binaire Go, client en ligne de
# commande. Idempotent — le relancer après un `git pull` reconstruit ce qu'il faut.
set -euo pipefail

RACINE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
INSTALLER_CLI=1
VERIFIER=1
CUDA=0

usage() {
  cat <<'USAGE'
Usage: ./install.sh [options]

  --no-cli     N'installe pas le client `gladyss` (et `say`) dans le PATH
  --no-check   Saute la vérification finale (qui charge le modèle et parle)
  --bin-dir D  Où installer le client (défaut: ~/.local/bin)
  --cuda       Installe torch avec CUDA (Linux, GPU NVIDIA). Par défaut la
               variante CPU, 2,7 Go de moins et suffisante pour ce daemon.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-cli)   INSTALLER_CLI=0; shift ;;
    --no-check) VERIFIER=0; shift ;;
    --cuda)     CUDA=1; shift ;;
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
for outil in python3 go ffmpeg ffplay curl; do
  command -v "$outil" >/dev/null 2>&1 || manquants+=("$outil")
done

if [ ${#manquants[@]} -gt 0 ]; then
  echo "Manquants : ${manquants[*]}"
  if [ "$SYSTEME" = macos ]; then
    echo "  brew install python go ffmpeg curl"
  elif command -v pacman >/dev/null 2>&1; then
    echo "  sudo pacman -S python go ffmpeg curl"
  elif command -v apt-get >/dev/null 2>&1; then
    echo "  sudo apt-get install python3 python3-venv golang ffmpeg curl"
  elif command -v dnf >/dev/null 2>&1; then
    echo "  sudo dnf install python3 golang ffmpeg curl"
  fi
  echec "installe ces outils puis relance ./install.sh"
fi
echo "python3 $(python3 --version 2>&1 | cut -d' ' -f2), $(go version | cut -d' ' -f3), ffmpeg présent"

# --- Environnement Python -----------------------------------------------------
# uv est nettement plus rapide pour installer torch, et c'est lui qui a créé le
# venv sur les machines qui l'ont : ses venv n'embarquent pas pip, donc on ne
# peut pas se contenter de `.venv/bin/pip`.
etape "Environnement Python du daemon"
VENV="$RACINE/.venv"

# torch d'abord, et depuis le bon index. Sur Linux, la roue PyPI par défaut
# emporte les paquets CUDA : 2,7 Go de plus, inutiles sans GPU NVIDIA — et le
# daemon tourne très bien sur CPU. Sur macOS la roue PyPI est déjà sans CUDA.
TORCH_INDEX=()
if [ "$SYSTEME" = linux ] && [ "$CUDA" -eq 0 ]; then
  TORCH_INDEX=(--index-url https://download.pytorch.org/whl/cpu)
  echo "torch : variante CPU (--cuda pour la variante NVIDIA)"
fi

if command -v uv >/dev/null 2>&1; then
  [ -d "$VENV" ] || uv venv "$VENV"
  uv pip install --python "$VENV/bin/python" "${TORCH_INDEX[@]}" torch
  uv pip install --python "$VENV/bin/python" -r "$RACINE/requirements.txt"
else
  [ -d "$VENV" ] || python3 -m venv "$VENV"
  [ -x "$VENV/bin/pip" ] || "$VENV/bin/python" -m ensurepip --upgrade >/dev/null
  "$VENV/bin/python" -m pip install --quiet --upgrade pip
  "$VENV/bin/python" -m pip install --quiet "${TORCH_INDEX[@]}" torch
  "$VENV/bin/python" -m pip install --quiet -r "$RACINE/requirements.txt"
fi

"$VENV/bin/python" -c 'import pocket_tts, numpy, torch' \
  || echec "les dépendances Python ne s'importent pas"
# Une dépendance transitive peut avoir rappelé la roue CUDA par-dessus la
# nôtre : le dire plutôt que de laisser 2,7 Go s'installer en silence.
if [ "${TORCH_INDEX[*]:-}" != "" ]; then
  version=$("$VENV/bin/python" -c 'import torch; print(torch.__version__)')
  case "$version" in
    *cu*) echo "attention : torch $version (CUDA) a été réinstallé par une dépendance" ;;
    *)    echo "torch $version" ;;
  esac
fi
echo "dépendances à jour"

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
  echo "énoncé envoyé — tu devrais l'entendre d'ici une trentaine de secondes"

  # /voices ne répond qu'une fois le moteur réveillé par l'énoncé ci-dessus.
  compte=""
  for _ in $(seq 120); do
    compte=$(curl -s --max-time 2 http://127.0.0.1:8420/voices \
      | python3 -c 'import json,sys; v=json.load(sys.stdin).get("voices"); print(len(v) if v else "")' 2>/dev/null || true)
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

Le clonage de voix demande un accès au dépôt kyutai/pocket-tts sur HuggingFace
(conditions à accepter, puis .venv/bin/hf auth login). Cf. « Cloner une voix »
dans le README. Sans cela, les 26 voix du catalogue restent disponibles.
EOF
