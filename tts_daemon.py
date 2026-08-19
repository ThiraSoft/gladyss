#!/usr/bin/env python3
"""Daemon de synthèse vocale Kyutai Pocket TTS.

Charge le modèle une seule fois, puis lit des commandes JSON sur stdin et émet
de l'audio PCM sur stdout. Le service Go pilote ce processus.

Protocole stdin — une commande JSON par ligne :
    {"cmd": "say", "text": "...", "voice": "estelle"}
    {"cmd": "cancel"}

Protocole stdout — un en-tête JSON par ligne, suivi pour "audio" de N octets bruts :
    {"type": "ready", "sample_rate": 24000, "voices": [...]}
    {"type": "start"}
    {"type": "audio", "bytes": N}\n<N octets PCM signés 16 bits little-endian>
    {"type": "end", "cancelled": false}
    {"type": "error", "message": "..."}

Tous les journaux vont sur stderr : stdout ne transporte que le protocole.
"""

import json
import os
import queue
import re
import sys
import threading
from pathlib import Path

import numpy as np
import torch

LANGUE = "french_24l"  # seule variante française publiée par Kyutai

# Voix préchargée au démarrage, et repli si une commande n'en précise aucune. Le
# service Go la renseigne d'après son propre `-voice` : préparer une autre voix
# que celle qui va servir ne ferait que retarder le premier énoncé.
VOIX_DEFAUT = os.environ.get("GLADYSS_DEFAULT_VOICE") or os.environ.get("SAY_DEFAULT_VOICE") or "estelle"

# Seuil de détection de fin de parole, réglé par le service (cf. son -eos-threshold).
#
# La bibliothèque utilise -4,0 : `out_eos > seuil` sur un logit, donc la fin est
# déclarée dès ~2 % de probabilité. Sur une phrase de quatre mots, le modèle
# s'arrête alors en pleine syllabe une fois sur deux — mesuré sur « Mais je sais
# pas » : 0,16 s de parole au lieu de 1,04 s. Les auteurs prévoyaient de rallonger
# les entrées courtes (`pad_with_spaces_for_short_inputs`), mais l'option est
# désactivée pour le français et le texte rembourré est de toute façon jeté par
# `split_into_best_sentences`, qui le passe au strip.
SEUIL_EOS = float(os.environ.get("GLADYSS_EOS_THRESHOLD") or os.environ.get("SAY_EOS_THRESHOLD") or 0.0)

# Voix clonées : un WAV de référence par voix, nommé d'après elle.
REPERTOIRE_VOIX = Path(__file__).resolve().parent / "voix"

# Regroupe les segments courts pour éviter une prosodie hachée.
LONGUEUR_MIN_SEGMENT = 60

_ecriture = threading.Lock()


def voix_predefinies() -> list[str]:
    """Catalogue des voix prédéfinies, lu dans le paquet plutôt que codé en dur.

    L'attribut est privé côté pocket_tts : en cas de renommage, on préfère une
    liste vide (le service désactive alors la validation) à un plantage.
    """
    try:
        from pocket_tts.models.tts_model import _ORIGINS_OF_PREDEFINED_VOICES

        return sorted(_ORIGINS_OF_PREDEFINED_VOICES)
    except Exception as exc:
        log(f"catalogue de voix indisponible ({exc}) : validation désactivée")
        return []


def voix_clonees() -> dict[str, Path]:
    """Voix locales trouvées dans voix/, du nom vers son fichier de référence.

    Un `.safetensors` est l'état du modèle déjà calculé : on le préfère au WAV,
    qui coûte quelques secondes d'encodage. Le daemon écrit ce cache lui-même au
    premier usage du WAV, il n'y a donc rien à générer à la main.
    """
    trouvees: dict[str, Path] = {}
    if not REPERTOIRE_VOIX.is_dir():
        return trouvees
    # Le WAV d'abord, le cache ensuite : à noms égaux, le cache l'emporte.
    for motif in ("*.wav", "*.safetensors"):
        for fichier in sorted(REPERTOIRE_VOIX.glob(motif)):
            # Les « ._nom » sont les métadonnées AppleDouble déposées par macOS
            # à côté des vrais fichiers : 163 octets qui ne sont ni un WAV ni un
            # état de modèle. Sans ce filtre, le catalogue annonce une voix
            # « ._gladyss » qui ne peut que faire échouer la synthèse.
            if fichier.name.startswith("._"):
                continue
            trouvees[fichier.stem] = fichier
    return trouvees


def voix_disponibles() -> list[str]:
    """Catalogue complet annoncé au service : voix prédéfinies et voix clonées."""
    return sorted(set(voix_predefinies()) | set(voix_clonees()))


def log(message: str) -> None:
    print(f"[tts] {message}", file=sys.stderr, flush=True)


def emettre(entete: dict, charge: bytes = b"") -> None:
    """Écrit un message du protocole sur stdout, de façon atomique."""
    with _ecriture:
        sys.stdout.buffer.write(json.dumps(entete).encode() + b"\n")
        if charge:
            sys.stdout.buffer.write(charge)
        sys.stdout.buffer.flush()


def decouper(texte: str) -> list[str]:
    """Découpe le texte en segments prononçables.

    Un texte long généré d'un bloc dérive en qualité et retarde l'interruption :
    on synthétise phrase par phrase, en fusionnant les fragments trop courts.
    """
    morceaux = re.split(r"(?<=[.!?…:;])\s+|\n+", texte.strip())
    segments: list[str] = []
    for morceau in (m.strip() for m in morceaux if m and m.strip()):
        if segments and len(segments[-1]) < LONGUEUR_MIN_SEGMENT:
            segments[-1] = f"{segments[-1]} {morceau}"
        else:
            segments.append(morceau)
    return segments or ([texte.strip()] if texte.strip() else [])


def en_pcm16(chunk: torch.Tensor) -> bytes:
    """Convertit un chunk float32 [-1, 1] en PCM signé 16 bits little-endian."""
    echantillons = chunk.detach().cpu().numpy().reshape(-1)
    return (np.clip(echantillons, -1.0, 1.0) * 32767.0).astype("<i2").tobytes()


class Daemon:
    def __init__(self) -> None:
        from pocket_tts import TTSModel

        log(f"chargement du modèle ({LANGUE}, seuil EOS {SEUIL_EOS})…")
        self.model = TTSModel.load_model(language=LANGUE, eos_threshold=SEUIL_EOS)
        self.voix: dict[str, dict] = {}
        self.commandes: queue.Queue[dict] = queue.Queue()
        self.annulation = threading.Event()
        self.precharger(VOIX_DEFAUT)
        log(f"prêt — {self.model.sample_rate} Hz")

    def precharger(self, nom: str) -> dict:
        """Rend l'état du modèle pour une voix, en le calculant au besoin.

        Une voix clonée est désignée par un fichier de voix/ ; une voix
        prédéfinie, par son seul nom, que pocket_tts sait résoudre.
        """
        if nom in self.voix:
            return self.voix[nom]

        reference = voix_clonees().get(nom)
        if reference is None:
            log(f"préparation de la voix {nom!r}…")
            self.voix[nom] = self.model.get_state_for_audio_prompt(nom)
            return self.voix[nom]

        if not getattr(self.model, "has_voice_cloning", True) and reference.suffix == ".wav":
            raise RuntimeError(
                f"clonage indisponible : les poids de kyutai/pocket-tts n'ont pas pu être "
                f"téléchargés. Accepte les conditions sur "
                f"https://huggingface.co/kyutai/pocket-tts puis connecte-toi avec "
                f"`.venv/bin/hf auth login`, et relance le service."
            )

        if reference.suffix == ".safetensors":
            log(f"voix clonée {nom!r} relue depuis {reference.name}")
        else:
            log(f"clonage de la voix {nom!r} depuis {reference.name}…")
        etat = self.model.get_state_for_audio_prompt(str(reference))
        self.voix[nom] = etat

        # Encoder le WAV coûte quelques secondes à chaque démarrage : on écrit
        # l'état à côté pour que les lancements suivants le relisent directement.
        if reference.suffix == ".wav":
            cache = reference.with_suffix(".safetensors")
            try:
                from pocket_tts.models.tts_model import export_model_state

                export_model_state(etat, cache)
                log(f"état mis en cache dans {cache.name}")
            except Exception as exc:  # cache absent = lenteur, pas une panne
                log(f"cache de la voix {nom!r} non écrit ({exc})")

        return etat

    def lire_stdin(self) -> None:
        """Thread dédié : dépile stdin sans jamais bloquer la synthèse.

        « cancel » lève un drapeau consulté entre deux chunks, ce qui permet
        d'interrompre une génération déjà lancée.
        """
        for ligne in sys.stdin:
            ligne = ligne.strip()
            if not ligne:
                continue
            try:
                commande = json.loads(ligne)
            except json.JSONDecodeError:
                emettre({"type": "error", "message": f"commande illisible : {ligne[:80]}"})
                continue

            if commande.get("cmd") == "cancel":
                self.annulation.set()
            else:
                self.commandes.put(commande)
        self.commandes.put({"cmd": "quit"})

    def synthetiser(self, commande: dict) -> None:
        texte = (commande.get("text") or "").strip()
        if not texte:
            emettre({"type": "error", "message": "texte vide"})
            emettre({"type": "end", "cancelled": False})
            return

        try:
            etat = self.precharger(commande.get("voice") or VOIX_DEFAUT)
        except Exception as exc:  # voix inconnue, clonage impossible, téléchargement échoué
            emettre({"type": "error", "message": f"voix indisponible : {exc}"})
            emettre({"type": "end", "cancelled": False})
            return

        emettre({"type": "start"})
        annule = False
        for segment in decouper(texte):
            if self.annulation.is_set():
                annule = True
                break
            try:
                for chunk in self.model.generate_audio_stream(etat, segment):
                    if self.annulation.is_set():
                        annule = True
                        break
                    charge = en_pcm16(chunk)
                    emettre({"type": "audio", "bytes": len(charge)}, charge)
            except Exception as exc:
                emettre({"type": "error", "message": f"échec de synthèse : {exc}"})
                break
            if annule:
                break

        emettre({"type": "end", "cancelled": annule})

    def boucler(self) -> None:
        threading.Thread(target=self.lire_stdin, daemon=True).start()
        emettre({
            "type": "ready",
            "sample_rate": self.model.sample_rate,
            "language": LANGUE,
            "voices": voix_disponibles(),
        })

        while True:
            commande = self.commandes.get()
            if commande.get("cmd") == "quit":
                return
            if commande.get("cmd") != "say":
                emettre({"type": "error", "message": f"commande inconnue : {commande.get('cmd')!r}"})
                continue
            # Un cancel arrivé pendant l'inactivité ne doit pas tuer l'énoncé suivant.
            self.annulation.clear()
            self.synthetiser(commande)


if __name__ == "__main__":
    try:
        Daemon().boucler()
    except KeyboardInterrupt:
        pass
