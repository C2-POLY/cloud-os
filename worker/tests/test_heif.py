"""The libheif binding: sniffing, decoding and EXIF of a real HEIC."""

from pathlib import Path

import pytest
from PIL import Image

from worker import heif

PHOTO = (Path(__file__).parent / "fixtures" / "photo.heic").read_bytes()


def ftyp(brand: bytes) -> bytes:
    return b"\x00\x00\x00\x18ftyp" + brand + b"\x00\x00\x00\x00mif1heic"


@pytest.mark.parametrize("brand", [b"heic", b"heix", b"mif1", b"msf1"])
def test_heif_brands_are_recognised(brand: bytes) -> None:
    assert heif.is_heif(ftyp(brand))


@pytest.mark.parametrize(
    "data",
    [ftyp(b"avif"), ftyp(b"isom"), b"\xff\xd8\xff\xe0", b"", b"\x00\x00\x00\x18ftyphe"],
    ids=["avif", "mp4", "jpeg", "empty", "short"],
)
def test_other_files_are_not_heif(data: bytes) -> None:
    assert not heif.is_heif(data)


def test_the_library_loads() -> None:
    assert heif.version().startswith("1.")


def test_open_image_decodes_the_pixels() -> None:
    im = heif.open_image(PHOTO)
    assert (im.mode, im.size) == ("RGB", (64, 48))
    # The red rectangle top left and the yellow disc bottom right, give or take
    # the lossy coding.
    r, g, b = im.getpixel((5, 5))
    assert r > 180 and g < 90 and b < 90
    r, g, b = im.getpixel((48, 32))
    assert r > 200 and g > 180 and b < 90


def test_probe_reads_size_and_exif_without_decoding() -> None:
    width, height, tiff = heif.probe(PHOTO)
    assert (width, height) == (64, 48)
    assert tiff and tiff[:4] in (b"MM\x00*", b"II*\x00")
    exif = Image.Exif()
    exif.load(tiff)
    assert exif[0x0110] == "iPhone 15"


@pytest.mark.parametrize(
    "data",
    [PHOTO[:600], ftyp(b"heic") + b"\x00" * 64, PHOTO[:32] + b"\xff" * (len(PHOTO) - 32)],
    ids=["truncated", "header only", "garbage after the header"],
)
def test_broken_files_raise_heif_error(data: bytes) -> None:
    with pytest.raises(heif.HeifError):
        heif.open_image(data)


def test_bombs_are_refused_before_decoding(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(Image, "MAX_IMAGE_PIXELS", 1000)
    with pytest.raises(Image.DecompressionBombError):
        heif.open_image(PHOTO)


def test_missing_library_is_a_retryable_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(heif, "_lib", None)
    monkeypatch.setattr(heif, "LIBRARIES", ("libheif-that-does-not-exist.so",))
    monkeypatch.setattr(heif.ctypes.util, "find_library", lambda _: None)
    with pytest.raises(RuntimeError, match="libheif not found"):
        heif.open_image(PHOTO)
