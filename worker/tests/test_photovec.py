"""The photovec task: what photos and videos show, as SigLIP 2 vectors in
pgvector, against a throwaway database and data root; and, end to end, the
benchmark's French queries finding their photos through the query endpoint.

The encoding tests need the models (`make models`) and, for videos, ffmpeg
(`make ffmpeg`); without them they are skipped, except in CI."""

import hashlib
import io
import json
import os
import shutil
import threading
import tomllib
import urllib.request
from pathlib import Path

import psycopg
import pytest
from PIL import Image

from worker import embedserver, models, photovec, tasks, video

FIXTURES = Path(__file__).parent / "fixtures"
BENCH = FIXTURES / "bench"
HEIC = (FIXTURES / "photo.heic").read_bytes()
CLIP = (FIXTURES / "clip.mp4").read_bytes()
DOG = (BENCH / "photos" / "chien-plage-1.jpg").read_bytes()
# core/internal/photovec MinScore: no expected photo of the benchmark under it.
MIN_SCORE = 0.06

needs_models = pytest.mark.skipif(
    not (models.models_dir() / "SHA256SUMS").exists()
    and not os.environ.get("CLOUDOS_REQUIRE_MODELS"),
    reason="no models: run `make models`",
)
ffmpeg = pytest.mark.skipif(
    shutil.which(video.binary("ffprobe")) is None and not os.environ.get("CLOUDOS_REQUIRE_FFMPEG"),
    reason="no ffmpeg: run `make ffmpeg`",
)


@pytest.fixture
def root(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("CLOUDOS_DATA_DIR", str(tmp_path))
    return tmp_path


def store(
    conn: psycopg.Connection, root: Path, path: str, data: bytes, mime: str = "image/jpeg"
) -> bytes:
    """Put a file on disk and in the index, as core/internal/storage does."""
    disk = root / "enzo" / path
    disk.parent.mkdir(parents=True, exist_ok=True)
    disk.write_bytes(data)
    sha = hashlib.sha256(data).digest()
    conn.execute(
        """INSERT INTO files (owner, path, size, mime, sha256, modified_at)
           VALUES ('enzo', %s, %s, %s, %s, now())""",
        (path, len(data), mime, sha),
    )
    return sha


def run(conn: psycopg.Connection, sha: bytes) -> tuple[str, str | None]:
    task_id = tasks.enqueue(conn, photovec.KIND, {"sha256": sha.hex()})
    assert tasks.run_one(conn, "w", tasks.HANDLERS)
    row = conn.execute("SELECT state, last_error FROM tasks WHERE id = %s", (task_id,)).fetchone()
    assert row
    return row[0], row[1]


def stored(conn: psycopg.Connection, sha: bytes) -> tuple | None:
    """model, version, source, dimension, length and creation of a vector."""
    return conn.execute(
        f"""SELECT model, version, source, vector_dims(embedding), vector_norm(embedding),
                   created_at
            FROM {photovec.TABLE} WHERE sha256 = %s""",
        (sha,),
    ).fetchone()


def test_transparency_turns_white_not_black() -> None:
    clear = Image.new("RGBA", (4, 4), (0, 0, 0, 0))
    assert photovec.upright_rgb(clear).getpixel((1, 1)) == (255, 255, 255)
    assert photovec.upright_rgb(Image.new("L", (4, 4), 9)).getpixel((1, 1)) == (9, 9, 9)


def test_a_vector_of_the_current_version_is_not_made_again(
    conn: psycopg.Connection, root: Path
) -> None:
    sha = store(conn, root, "a.jpg", b"never read")
    conn.execute(
        f"""INSERT INTO {photovec.TABLE} (sha256, model, version, source, embedding)
            VALUES (%s, %s, %s, 'image', array_fill(0.036::real, ARRAY[768])::vector)""",
        (sha, photovec.MODEL, photovec.VERSION),
    )
    assert run(conn, sha) == ("done", None)


def test_unreadable_image_fails_for_good(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "bad.jpg", b"not a jpeg at all")
    state, error = run(conn, sha)
    assert state == "failed" and error and "unreadable image" in error
    assert stored(conn, sha) is None


def test_content_no_longer_stored_is_done(conn: psycopg.Connection, root: Path) -> None:
    assert run(conn, hashlib.sha256(b"gone").digest()) == ("done", None)
    assert conn.execute(f"SELECT count(*) FROM {photovec.TABLE}").fetchone() == (0,)


@needs_models
def test_task_encodes_a_photo_and_a_heic_and_never_writes_a_file(
    conn: psycopg.Connection, root: Path
) -> None:
    jpg = store(conn, root, "Plage/chien.jpg", DOG)
    heic = store(conn, root, "iPhone/IMG_0001.HEIC", HEIC, "image/heic")
    png = io.BytesIO()
    Image.new("RGBA", (300, 200), (0, 120, 255, 128)).save(png, "PNG")
    pngsha = store(conn, root, "Captures/ciel.png", png.getvalue(), "image/png")
    for sha in (jpg, heic, pngsha):
        assert run(conn, sha) == ("done", None)
        row = stored(conn, sha)
        assert row and row[:4] == (models.IMAGE_MODEL, models.IMAGE_MODEL_VERSION, "image", 768)
        assert abs(row[4] - 1) < 1e-4
    assert (root / "enzo" / "Plage" / "chien.jpg").read_bytes() == DOG
    assert (root / "enzo" / "iPhone" / "IMG_0001.HEIC").read_bytes() == HEIC
    assert sorted(p.name for p in root.rglob("*") if p.is_file()) == [
        "IMG_0001.HEIC",
        "chien.jpg",
        "ciel.png",
    ]
    # Running it again is a no-op.
    before = stored(conn, jpg)
    assert run(conn, jpg) == ("done", None)
    assert stored(conn, jpg) == before


@needs_models
@ffmpeg
def test_task_encodes_a_video_by_its_key_frame(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "Vidéos/clip.mp4", CLIP, "video/mp4")
    assert run(conn, sha) == ("done", None)
    row = stored(conn, sha)
    assert row and row[2:4] == ("video", 768)
    # The same frame the thumbnail shows.
    frame = photovec.upright_rgb(
        photovec.thumbs.decode(video.frame(root / "enzo" / "Vidéos" / "clip.mp4"))
    )
    expected = models.image_encoder().embed([frame])[0]
    got = conn.execute(
        f"SELECT embedding::real[] FROM {photovec.TABLE} WHERE sha256 = %s", (sha,)
    ).fetchone()
    assert got and max(abs(a - b) for a, b in zip(got[0], expected, strict=True)) < 1e-5
    assert (root / "enzo" / "Vidéos" / "clip.mp4").read_bytes() == CLIP


@needs_models
def test_a_vector_of_an_older_version_is_made_again(conn: psycopg.Connection, root: Path) -> None:
    sha = store(conn, root, "chien.jpg", DOG)
    conn.execute(
        f"""INSERT INTO {photovec.TABLE} (sha256, model, version, source, embedding, created_at)
            VALUES (%s, %s, 'older', 'image', array_fill(0.036::real, ARRAY[768])::vector,
                    now() - interval '1 day')""",
        (sha, photovec.MODEL),
    )
    assert run(conn, sha) == ("done", None)
    row = stored(conn, sha)
    assert row and row[1] == models.IMAGE_MODEL_VERSION
    old = conn.execute(
        f"""SELECT count(*) FROM {photovec.TABLE}
            WHERE embedding = array_fill(0.036::real, ARRAY[768])::vector"""
    ).fetchone()
    assert old == (0,)


def search(conn: psycopg.Connection, url: str, q: str) -> list[tuple[str, float]]:
    """What the core does for GET /api/search/photos (core/internal/photovec
    Search): the endpoint encodes q, pgvector orders the photos by cosine."""
    body = json.dumps({"model": models.IMAGE_MODEL, "texts": [q]}).encode()
    req = urllib.request.Request(url + "/embed", data=body, method="POST")
    with urllib.request.urlopen(req, timeout=60) as res:
        answer = json.loads(res.read())
    rows = conn.execute(
        f"""SELECT f.path, 1 - (v.embedding <=> %s::vector) AS score
            FROM {photovec.TABLE} v JOIN files f ON f.sha256 = v.sha256
            WHERE v.version = %s AND f.owner = 'enzo' AND f.trash_id IS NULL
            ORDER BY v.embedding <=> %s::vector""",
        (str(answer["vectors"][0]), answer["version"], str(answer["vectors"][0])),
    ).fetchall()
    return [(Path(p).stem, s) for p, s in rows]


@needs_models
def test_french_queries_find_their_photos_end_to_end(conn: psycopg.Connection, root: Path) -> None:
    for p in sorted((BENCH / "photos").glob("*.jpg")):
        assert run(conn, store(conn, root, f"Photos/{p.name}", p.read_bytes())) == ("done", None)
    stop = threading.Event()
    server = embedserver.start(stop, ("127.0.0.1", 0))
    url = f"http://127.0.0.1:{server.server_address[1]}"
    try:
        expected = {
            q["q"]: q["expect"]
            for q in tomllib.loads((BENCH / "queries.toml").read_text())["image"]
        }
        found = search(conn, url, "chien à la plage")
        assert sorted(name for name, _ in found[:3]) == [
            "chien-plage-1",
            "chien-plage-2",
            "chien-plage-3",
        ]
        assert all(score >= MIN_SCORE for _, score in found[:3])
        assert search(conn, url, "coucher de soleil")[0][0] == "coucher-soleil"
        # The int8 models do not compute the same on every CPU: an x86-64 without
        # VNNI saturates the text tower's int8 products, and the activations'
        # quantization noise moves a score by up to 0.03. The near-tie « parasols
        # sur la plage » (plage ahead by 0.013 on arm64, by 0.054 in fp32) puts a
        # dog on a beach first there. So, as worker/bench measures it: the right
        # photos first for all queries but one, and never two photos ahead of them.
        firsts = 0
        for q, want in expected.items():
            found = search(conn, url, q)
            names = [n for n, _ in found]
            last = max(names.index(w) for w in want) + 1
            assert last <= len(want) + 1, (q, names[: len(want) + 1])
            firsts += last == len(want)
            assert min(s for n, s in found if n in want) >= MIN_SCORE, q
        assert firsts >= len(expected) - 1
    finally:
        stop.set()
