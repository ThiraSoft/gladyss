#!/usr/bin/env bash
# Monte un prompt de clonage à partir d'une liste de clips.
#
#   ./preparer_voix.sh gladyss      # voix/gladyss.liste -> voix/gladyss.wav
#
# La liste donne un clip par ligne, relatif à $SOURCES (ou absolu) ; les lignes
# vides et celles qui commencent par # sont ignorées. Chaque clip est ramené au
# même niveau RMS et rogné de ses silences de bord, puis les clips sont mis bout
# à bout avec un blanc entre deux. Le tout sort en 24 kHz mono 16 bits, le
# format natif du modèle, avec une marge de crête.
set -euo pipefail
export LC_ALL=C  # des points décimaux pour awk, printf et sox

RACINE=$(cd "$(dirname "$0")" && pwd)
SOURCES=${SOURCES:-$RACINE/ref/glados/sounds}
BLANC=${BLANC:-0.2}          # silence entre deux clips, en secondes
CIBLE_RMS=${CIBLE_RMS:--13}  # niveau commun, en dBFS
MARGE=${MARGE:--1}           # crête maximale, en dBFS

nom=${1:?usage: $0 <nom>   (lit voix/<nom>.liste)}
liste=$RACINE/voix/$nom.liste
sortie=$RACINE/voix/$nom.wav
[[ -f $liste ]] || { echo "pas de liste : $liste" >&2; exit 1; }
command -v sox >/dev/null || { echo "sox est requis" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Les sources de jeu arrivent normalisées à 0 dBFS, et sox écrête à chaque
# sortie, même en flottant : tout le montage se fait RESERVE dB plus bas, rendus
# à la fin dans la limite de la marge de crête.
RESERVE=10
morceaux=()
i=0
while IFS= read -r ligne || [[ -n $ligne ]]; do
  ligne=${ligne%%#*}
  ligne=$(echo "$ligne" | xargs)
  [[ -z $ligne ]] && continue
  [[ $ligne = /* ]] && src=$ligne || src=$SOURCES/$ligne
  [[ -f $src ]] || { echo "clip introuvable : $src" >&2; exit 1; }

  i=$((i + 1))
  rogne=$tmp/$i.rogne.wav
  sox "$src" -e floating-point -b 32 "$rogne" remix - gain -$RESERVE rate -v 24000 \
    silence 1 0.02 -50d reverse silence 1 0.02 -50d reverse
  rms=$(sox "$rogne" -n stats 2>&1 | awk '/^RMS lev dB/ {print $4}')
  gain=$(awk -v c="$CIBLE_RMS" -v r="$rms" -v res="$RESERVE" 'BEGIN {printf "%.2f", c - r - res}')
  sox "$rogne" "$tmp/$i.wav" gain "$gain"
  [[ $i -gt 1 ]] && morceaux+=("$tmp/blanc.wav")
  morceaux+=("$tmp/$i.wav")
  printf '  %-60s %6.1f dB\n' "$ligne" "$gain"
done < "$liste"

[[ $i -gt 0 ]] || { echo "liste vide : $liste" >&2; exit 1; }
sox -n -r 24000 -c 1 -e floating-point -b 32 "$tmp/blanc.wav" trim 0 "$BLANC"
sox "${morceaux[@]}" "$tmp/tout.wav"

# La réserve revient, sauf ce qu'il faut garder pour ne pas dépasser la marge.
pic=$(sox "$tmp/tout.wav" -n stats 2>&1 | awk '/^Pk lev dB/ {print $4}')
rendu=$(awk -v m="$MARGE" -v p="$pic" -v r="$RESERVE" 'BEGIN {g = m - p; printf "%.2f", (g < r) ? g : r}')
sox "$tmp/tout.wav" -e signed-integer -b 16 "$sortie" gain "$rendu" dither

# L'état cloné est dérivé du WAV : un cache calculé sur l'ancien montage
# l'emporterait sur le nouveau.
rm -f "$RACINE/voix/$nom.safetensors"

duree=$(soxi -D "$sortie")
printf '%s : %d clips, %.1f s\n' "${sortie#"$RACINE"/}" "$i" "$duree"
