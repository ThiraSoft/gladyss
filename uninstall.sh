#!/usr/bin/env bash
# Désinstalle gladyss : le client du PATH et le binaire construit. Ce qui a
# coûté à obtenir — les poids du modèle, les voix clonées, la configuration —
# n'est retiré qu'après confirmation, une question par catégorie.
set -euo pipefail

RACINE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
SANS_QUESTION=0

CACHE="$HOME/.cache/huggingface/hub/models--kyutai--pocket-tts-without-voice-cloning"
CONFIG="$HOME/.config/gladyss/config.json"

usage() {
  cat <<'USAGE'
Usage: ./uninstall.sh [options]

Retire sans demander : le client `gladyss` du PATH et le binaire construit.
Demande avant de retirer : les poids du modèle, les voix clonées, la
configuration utilisateur.

  --yes        Répond oui à toutes les questions — retire tout, sans demander
  --bin-dir D  Où le client a été installé (défaut: ~/.local/bin)
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes|-y)  SANS_QUESTION=1; shift ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Option inconnue : $1" >&2; usage; exit 1 ;;
  esac
done

etape() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

# demander pose une question dont la réponse par défaut est non : ce script
# supprime, et le silence ne doit jamais valoir consentement. Sans terminal —
# dans un script, un conteneur — la réponse est non, et on le dit.
demander() {
  if [ "$SANS_QUESTION" -eq 1 ]; then
    return 0
  fi
  if [ ! -t 0 ]; then
    echo "  gardé (pas de terminal pour demander ; --yes pour retirer)"
    return 1
  fi
  printf '  %s [o/N] ' "$1"
  read -r reponse
  case "$reponse" in [oOyY]*) return 0 ;; *) return 1 ;; esac
}

# --- Client et binaire --------------------------------------------------------
etape "Client et binaire"
retire=0
for f in "$BIN_DIR/gladyss" "$BIN_DIR/say" "$RACINE/gladyss" "$RACINE/say"; do
  # say est l'ancien alias : il n'est plus installé, mais une installation
  # antérieure a pu en laisser un, et le désinstalleur doit le savoir.
  if [ -e "$f" ] || [ -L "$f" ]; then
    rm -f "$f"
    echo "retiré : $f"
    retire=$((retire + 1))
  fi
done
[ "$retire" -eq 0 ] && echo "rien à retirer"

# --- Poids du modèle ----------------------------------------------------------
etape "Poids du modèle"
if [ -d "$CACHE" ]; then
  taille=$(du -sh "$CACHE" 2>/dev/null | cut -f1)
  echo "$CACHE ($taille)"
  echo "  les retéléchargerait un ./install.sh"
  if demander "Retirer les poids et les voix du catalogue ?"; then
    rm -rf "$CACHE"
    echo "  retirés"
  else
    echo "  gardés"
  fi
else
  echo "aucun poids téléchargé par install.sh"
fi

# Le dépôt avec clonage, lui, n'a jamais été téléchargé par install.sh : c'est
# l'utilisateur qui est allé l'accepter et le chercher. On le signale, on n'y
# touche pas.
autre="$HOME/.cache/huggingface/hub/models--kyutai--pocket-tts"
if [ -d "$autre" ]; then
  echo "note : $autre est là aussi (poids de clonage, obtenus à la main) — laissé en place"
fi

# --- Voix clonées -------------------------------------------------------------
etape "Voix clonées"
voix=$(find "$RACINE/voix" -maxdepth 1 \( -name '*.wav' -o -name '*.safetensors' \) 2>/dev/null | wc -l)
if [ "$voix" -gt 0 ]; then
  echo "$voix fichier(s) dans $RACINE/voix"
  echo "  ce sont des enregistrements, ils ne se retéléchargent pas"
  if demander "Retirer les voix clonées ?"; then
    find "$RACINE/voix" -maxdepth 1 \( -name '*.wav' -o -name '*.safetensors' \) -delete
    echo "  retirées"
  else
    echo "  gardées"
  fi
else
  echo "aucune voix clonée"
fi

# --- Configuration ------------------------------------------------------------
etape "Configuration"
if [ -f "$CONFIG" ]; then
  echo "$CONFIG"
  if demander "Retirer la configuration ?"; then
    rm -f "$CONFIG"
    rmdir "$(dirname "$CONFIG")" 2>/dev/null || true
    echo "  retirée"
  else
    echo "  gardée"
  fi
else
  echo "aucune configuration utilisateur"
fi

etape "Terminé"
echo "Le dépôt lui-même reste : le supprimer retire ce qu'il reste de gladyss."
