"""HEIF and HEIC images (iPhone photos), for thumbs and meta.

A minimal ctypes binding to the system's libheif, loaded dynamically: the
worker image installs it from Debian with only its decoder plugins (libde265,
dav1d), never an encoder (docs/decisions/002-lgpl.md). Nothing is compiled
against it, so the library can be replaced, as the LGPL requires.

libheif applies the image's own transformations (irot, imir, clap): the pixels
it returns are upright, and its EXIF orientation must not be applied again.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import struct
import threading

from PIL import Image

# The major brands of HEIF still images (ISO/IEC 23008-12): HEVC-coded (HEIC)
# or any codec (mif1, msf1). As core/internal/storage SniffMIME.
BRANDS = {b"heic", b"heix", b"heim", b"heis", b"mif1", b"msf1"}

# Enums of libheif's C API (heif_image.h); stable since libheif 1.0.
COLORSPACE_RGB = 1
CHROMA_INTERLEAVED_RGB = 10
CHROMA_INTERLEAVED_RGBA = 11
CHANNEL_INTERLEAVED = 10

LIBRARIES = (
    "libheif.so.1",  # Debian, Ubuntu
    "/opt/homebrew/lib/libheif.1.dylib",  # macOS development, Homebrew
    "/usr/local/lib/libheif.1.dylib",
)


class HeifError(ValueError):
    """libheif could not read the file: broken or unsupported. Callers turn it
    into a Permanent error."""


class _Error(ctypes.Structure):
    _fields_ = [("code", ctypes.c_int), ("subcode", ctypes.c_int), ("message", ctypes.c_char_p)]


def is_heif(data: bytes) -> bool:
    """Whether data starts with the file-type box of a HEIF still image."""
    return len(data) >= 12 and data[4:8] == b"ftyp" and data[8:12] in BRANDS


_lib: ctypes.CDLL | None = None
_lock = threading.Lock()


def _load() -> ctypes.CDLL:
    """libheif, loaded and initialised once (heif_init loads the decoder plugins).
    Raises RuntimeError when it is not installed: a setup error, retried later,
    never a reason to fail a photo for good."""
    global _lib
    with _lock:
        if _lib is not None:
            return _lib
        names = [*LIBRARIES, ctypes.util.find_library("heif")]
        lib = None
        for name in names:
            if not name:
                continue
            try:
                lib = ctypes.CDLL(name)
                break
            except OSError:
                continue
        if lib is None:
            raise RuntimeError("libheif not found: install libheif1 and libheif-plugin-libde265")
        _declare(lib)
        _check(lib.heif_init(None))
        _lib = lib
        return lib


def _declare(lib: ctypes.CDLL) -> None:
    p, i, s = ctypes.c_void_p, ctypes.c_int, ctypes.c_size_t
    for name, res, args in (
        ("heif_init", _Error, [p]),
        ("heif_get_version", ctypes.c_char_p, []),
        ("heif_context_alloc", p, []),
        ("heif_context_free", None, [p]),
        ("heif_context_read_from_memory_without_copy", _Error, [p, p, s, p]),
        ("heif_context_get_primary_image_handle", _Error, [p, ctypes.POINTER(p)]),
        ("heif_image_handle_release", None, [p]),
        ("heif_image_handle_get_width", i, [p]),
        ("heif_image_handle_get_height", i, [p]),
        ("heif_image_handle_has_alpha_channel", i, [p]),
        ("heif_image_handle_get_list_of_metadata_block_IDs", i, [p, ctypes.c_char_p, p, i]),
        ("heif_image_handle_get_metadata_size", s, [p, ctypes.c_uint32]),
        ("heif_image_handle_get_metadata", _Error, [p, ctypes.c_uint32, p]),
        ("heif_decode_image", _Error, [p, ctypes.POINTER(p), i, i, p]),
        ("heif_image_release", None, [p]),
        ("heif_image_get_width", i, [p, i]),
        ("heif_image_get_height", i, [p, i]),
        (
            "heif_image_get_plane_readonly",
            ctypes.POINTER(ctypes.c_uint8),
            [p, i, ctypes.POINTER(i)],
        ),
    ):
        fn = getattr(lib, name)
        fn.restype, fn.argtypes = res, args


def version() -> str:
    return _load().heif_get_version().decode()


def _check(err: _Error) -> None:
    if err.code != 0:
        message = (err.message or b"").decode("utf-8", "replace")
        raise HeifError(f"libheif {err.code}.{err.subcode}: {message}")


class _Primary:
    """The primary image of a HEIF file, open while in the with block."""

    def __init__(self, data: bytes) -> None:
        if not is_heif(data):
            raise HeifError("not a HEIF image")
        self.lib = _load()
        self.data = data  # libheif reads it in place: keep it alive
        self.ctx: int | None = None
        self.handle = ctypes.c_void_p()

    def __enter__(self) -> _Primary:
        lib = self.lib
        self.ctx = lib.heif_context_alloc()
        if not self.ctx:
            raise MemoryError("heif_context_alloc")
        try:
            _check(lib.heif_context_read_from_memory_without_copy(
                self.ctx, self.data, len(self.data), None))  # fmt: skip
            _check(lib.heif_context_get_primary_image_handle(self.ctx, ctypes.byref(self.handle)))
        except BaseException:
            self.__exit__()
            raise
        return self

    def __exit__(self, *_: object) -> None:
        if self.handle:
            self.lib.heif_image_handle_release(self.handle)
            self.handle = ctypes.c_void_p()
        if self.ctx:
            self.lib.heif_context_free(self.ctx)
            self.ctx = None

    def size(self) -> tuple[int, int]:
        """The size as displayed (transformations applied), without decoding."""
        return (
            self.lib.heif_image_handle_get_width(self.handle),
            self.lib.heif_image_handle_get_height(self.handle),
        )

    def exif(self) -> bytes | None:
        """The image's EXIF as a TIFF stream (what Image.Exif.load reads), or None."""
        lib, ids = self.lib, (ctypes.c_uint32 * 4)()
        n = lib.heif_image_handle_get_list_of_metadata_block_IDs(self.handle, b"Exif", ids, 4)
        for item in ids[: max(n, 0)]:
            size = lib.heif_image_handle_get_metadata_size(self.handle, item)
            if size <= 4:
                continue
            buf = ctypes.create_string_buffer(size)
            try:
                _check(lib.heif_image_handle_get_metadata(self.handle, item, buf))
            except HeifError:
                continue
            block = buf.raw
            # A 4-byte offset to the TIFF header, from the end of the offset.
            start = 4 + struct.unpack(">I", block[:4])[0]
            if start < len(block):
                return block[start:]
        return None

    def decode(self) -> Image.Image:
        """The pixels, upright, as RGB or RGBA."""
        lib = self.lib
        alpha = bool(lib.heif_image_handle_has_alpha_channel(self.handle))
        chroma, mode, depth = (
            (CHROMA_INTERLEAVED_RGBA, "RGBA", 4) if alpha else (CHROMA_INTERLEAVED_RGB, "RGB", 3)
        )
        img = ctypes.c_void_p()
        _check(lib.heif_decode_image(self.handle, ctypes.byref(img), COLORSPACE_RGB, chroma, None))
        try:
            width = lib.heif_image_get_width(img, CHANNEL_INTERLEAVED)
            height = lib.heif_image_get_height(img, CHANNEL_INTERLEAVED)
            stride = ctypes.c_int()
            plane = lib.heif_image_get_plane_readonly(
                img, CHANNEL_INTERLEAVED, ctypes.byref(stride)
            )
            if not plane or width <= 0 or height <= 0 or stride.value < width * depth:
                raise HeifError("libheif returned no usable pixels")
            # Copy out before the image is released; size from the plane itself.
            raw = ctypes.string_at(plane, stride.value * height)
        finally:
            lib.heif_image_release(img)
        return Image.frombuffer(mode, (width, height), raw, "raw", mode, stride.value, 1)


def _guard(width: int, height: int) -> None:
    """Refuse a decompression bomb before decoding it, as Pillow would."""
    limit = Image.MAX_IMAGE_PIXELS
    if width <= 0 or height <= 0:
        raise HeifError(f"bad image size {width}x{height}")
    if limit and width * height > limit:
        raise Image.DecompressionBombError(
            f"image size ({width * height} pixels) exceeds limit of {limit} pixels"
        )


def open_image(data: bytes) -> Image.Image:
    """The primary image of a HEIF file, upright, as RGB or RGBA. Raises
    HeifError for a broken or unsupported file, DecompressionBombError for one
    too large, RuntimeError when libheif is not installed."""
    with _Primary(data) as primary:
        _guard(*primary.size())
        return primary.decode()


def probe(data: bytes) -> tuple[int, int, bytes | None]:
    """The displayed size and the EXIF (TIFF stream) of a HEIF file, without
    decoding its pixels. Raises as open_image."""
    with _Primary(data) as primary:
        width, height = primary.size()
        _guard(width, height)
        return width, height, primary.exif()


if __name__ == "__main__":  # python -m worker.heif: the libheif in use
    print(f"libheif {version()}")
