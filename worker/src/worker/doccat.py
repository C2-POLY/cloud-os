"""The category of documents (#73), for core/internal/doccat.

Each category is described by a few French sentences (CATEGORIES), which
Arctic Embed encodes once per process, as searches (the "query: " prefix). A
document's vector is the mean of the vectors of its windows (worker.textemb);
a category scores the cosine of its nearest sentence, and the best category
wins, unless its score is under THRESHOLD: the document is then "autre". No
language model, no keyword: nothing but the vectors the search already has.

worker.textemb classifies a document with the vectors it has just computed,
in the transaction that stores them. The doccat task classifies a content
again from its stored vectors, without encoding it again: the core queues it
when the version of the classifier changes (another sentence, threshold or
model), which the worker declares in embedding_models (worker.vectors).

Measured on the benchmark (worker/tests/fixtures/bench, categories.toml):
30 documents out of 30 in their category, for any threshold from 0 to 0.20
(docs/architecture/documents.md).
"""

from __future__ import annotations

import hashlib
import json
import threading
from dataclasses import dataclass
from typing import TYPE_CHECKING

import numpy as np
import psycopg

from worker import models
from worker.content import sha_of

if TYPE_CHECKING:
    from worker.tasks import Task

KIND = "doccat"
# The name of the classifier in embedding_models: the core classifies every
# content again when its version changes (core/internal/doccat Sync).
NAME = "doccat"
DIMS = 768  # of the vectors it reads, Arctic's
TABLE = "document_categories"
# Under this cosine, no category is near enough: "autre". The right category
# of a benchmark document scores 0.20 at least, a recipe 0.10.
THRESHOLD = 0.15

# The categories, in the order the web shows them, each with sentences that
# describe its documents. The ids are those of the migration's CHECK
# (core/migrations/0022_document_categories.sql), core/internal/doccat and the
# web. Changing a sentence changes the version: every document is classified
# again, from its stored vectors.
CATEGORIES: dict[str, tuple[str, ...]] = {
    "factures": (
        "Une facture : le numéro de facture, la date, le détail des articles ou des "
        "prestations, la TVA et le total TTC à payer.",
        "Une facture d'électricité, de gaz, d'eau, d'internet ou de téléphone mobile.",
        "La facture d'un magasin, d'un garage, d'un artisan ou d'un vétérinaire.",
        "Un devis, un ticket de caisse, un reçu ou une quittance de loyer.",
    ),
    "contrats": (
        "Un contrat signé entre deux parties, avec ses conditions, sa durée et ses clauses.",
        "Un contrat d'assurance habitation, auto ou emprunteur, avec ses garanties "
        "et sa cotisation.",
        "Un bail de location d'un logement, un état des lieux ou un contrat de location "
        "saisonnière.",
        "Les conditions générales d'un abonnement ou d'un service.",
    ),
    "impots": (
        "Un avis d'impôt sur le revenu, de taxe foncière ou de taxe d'habitation.",
        "Une déclaration de revenus, le revenu fiscal de référence et le montant de "
        "l'impôt à payer.",
        "Un courrier de l'administration fiscale ou du centre des finances publiques.",
    ),
    "sante": (
        "Une ordonnance du médecin, avec les médicaments ou les soins prescrits.",
        "Une prescription de séances de kinésithérapie, de soins infirmiers ou d'analyses.",
        "Un compte rendu médical, une analyse de sang, une radio ou un examen à l'hôpital.",
        "Un carnet de santé ou de vaccination, une carte de mutuelle ou un remboursement "
        "de l'Assurance maladie.",
    ),
    "banque": (
        "Un relevé de compte bancaire avec les opérations, les virements et le solde.",
        "Un relevé d'épargne, un livret A, une assurance vie ou un plan d'épargne.",
        "Une offre de prêt immobilier, un crédit ou un tableau d'amortissement de la banque.",
    ),
    "travail": (
        "Un bulletin de salaire : le salaire brut, les cotisations et le net à payer.",
        "Un contrat de travail, une attestation employeur ou un certificat de travail.",
        "Un courrier de France Travail ou une attestation d'indemnisation chômage.",
    ),
    "administratif": (
        "Une attestation de la CAF, de la Sécurité sociale ou d'un organisme public.",
        "Une carte grise, un permis de conduire, une carte d'identité ou un passeport.",
        "Un certificat de scolarité, un acte de naissance ou un livret de famille.",
    ),
    "autre": (
        "Un billet de train ou d'avion, une réservation d'hôtel ou de voyage.",
        "Une recette de cuisine, une liste de courses ou des notes personnelles.",
        "Une lettre, un courrier personnel ou le compte rendu d'une réunion.",
    ),
}
OTHER = "autre"


@dataclass(frozen=True)
class Verdict:
    """A document's category; score is the best cosine found, margin how far
    ahead of the next category it is."""

    category: str
    score: float
    margin: float


def _described() -> str:
    """What decides a document's category besides its vectors: the sentences
    and the threshold, as text."""
    return json.dumps([THRESHOLD, CATEGORIES], ensure_ascii=False)


def version() -> str:
    """The version of the classification: the vectors it reads
    (worker.textemb.version()) and a digest of the sentences and threshold.
    A change to any of them classifies every document again."""
    from worker import textemb

    digest = hashlib.sha256(_described().encode()).hexdigest()[:8]
    return f"{textemb.version()}-c{digest}"


_lock = threading.Lock()
_prototypes: dict[str, tuple[tuple[str, ...], np.ndarray]] = {}


def prototypes() -> tuple[tuple[str, ...], np.ndarray]:
    """The category of each sentence, and the sentences' unit vectors, one row
    each. Encoded once per process (about 30 searches, half a second), one
    sentence at a time: adding a sentence never moves the others."""
    key = _described()
    with _lock:
        if key not in _prototypes:
            enc = models.text_encoder()
            labels = tuple(c for c, sentences in CATEGORIES.items() for _ in sentences)
            rows = [enc.embed([s], query=True)[0] for ss in CATEGORIES.values() for s in ss]
            _prototypes.clear()
            _prototypes[key] = (labels, np.stack(rows))
        return _prototypes[key]


def classify(windows: np.ndarray) -> Verdict:
    """The category of a document from the vectors of its windows, one row
    each: the mean of the windows, compared with every sentence."""
    doc = models.unit(windows.astype(np.float32).mean(axis=0))
    labels, protos = prototypes()
    sims = protos @ doc
    best: dict[str, float] = {}
    for label, sim in zip(labels, sims.tolist(), strict=True):
        best[label] = max(best.get(label, -1.0), sim)
    ranked = sorted(best.items(), key=lambda kv: kv[1], reverse=True)
    (first, score), (_, second) = ranked[0], ranked[1]
    return Verdict(first if score >= THRESHOLD else OTHER, score, score - second)


def store(cur: psycopg.Cursor, sha: bytes, verdict: Verdict, current: str) -> None:
    """Write the category of a content, replacing the one of another version."""
    cur.execute(
        f"""INSERT INTO {TABLE} (sha256, category, score, margin, version)
            VALUES (%s, %s, %s, %s, %s)
            ON CONFLICT (sha256) DO UPDATE
               SET category = EXCLUDED.category, score = EXCLUDED.score,
                   margin = EXCLUDED.margin, version = EXCLUDED.version, classified_at = now()""",
        (sha, verdict.category, verdict.score, verdict.margin, current),
    )


def make(task: Task, conn: psycopg.Connection) -> None:
    """The doccat task: payload {"sha256": hex}. Classifies the content again
    from its stored vectors; reads no file and encodes nothing but the
    sentences."""
    from worker import textemb

    sha = sha_of(task.payload)
    current = version()
    done = conn.execute(
        f"SELECT 1 FROM {TABLE} WHERE sha256 = %s AND version = %s", (sha, current)
    ).fetchone()
    if done:
        return
    rows = conn.execute(
        f"""SELECT embedding::text FROM {textemb.TABLE}
            WHERE sha256 = %s AND version = %s ORDER BY chunk""",
        (sha, textemb.version()),
    ).fetchall()
    if not rows:
        return  # no vectors of this version yet: textemb classifies it when it writes them
    windows = np.array([json.loads(r[0]) for r in rows], dtype=np.float32)
    with conn.cursor() as cur:
        store(cur, sha, classify(windows), current)
