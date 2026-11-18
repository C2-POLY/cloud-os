FROM ghcr.io/astral-sh/uv:0.6.10 AS uv

# ffmpeg and ffprobe for videos (#41): the official release, unmodified,
# LGPL only, decoders only (worker/ffmpeg/build.sh, docs/decisions/002-lgpl.md).
FROM python:3.13-slim AS ffmpeg
RUN apt-get update \
    && apt-get install --yes --no-install-recommends build-essential ca-certificates curl xz-utils zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
COPY ffmpeg/build.sh /build.sh
RUN /build.sh /opt/ffmpeg

FROM python:3.13-slim
COPY --from=uv /uv /bin/uv
COPY --from=ffmpeg /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe /opt/ffmpeg/bin/
ENV CLOUDOS_FFMPEG_DIR=/opt/ffmpeg/bin
# libheif reads HEIF and HEIC photos (worker/src/worker/heif.py loads it with
# ctypes). Decoders only, from Debian, unmodified and linked dynamically
# (docs/decisions/002-lgpl.md): libheif1 depends on the libde265 and dav1d
# plugins and only *recommends* the x265 and aom encoders, which
# --no-install-recommends leaves out. No GPL library enters the image.
RUN apt-get update \
    && apt-get install --yes --no-install-recommends libheif1 libheif-plugin-libde265 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
COPY pyproject.toml uv.lock .python-version ./
RUN uv sync --frozen --no-dev --no-install-project
COPY src ./src
RUN uv sync --frozen --no-dev
USER nobody
CMD ["/app/.venv/bin/python", "-m", "worker"]
