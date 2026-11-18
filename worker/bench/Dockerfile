# The benchmark of #66 on Linux, as on the box: the worker's base image, the
# worker's Python dependencies plus the bench group, and Tesseract from Debian
# (with the DejaVu fonts it pulls in, used to draw the pages). From the
# repository root:
#
#   docker build -t cloud-os-bench -f worker/bench/Dockerfile worker
#   docker run --rm -v "$PWD:/repo:ro" cloud-os-bench python -m bench text image
#   docker run --rm -v "$PWD:/repo:ro" cloud-os-bench python -m bench.ocr
#
# The repository is mounted read-only; the candidate models are read from
# .tools/bench-models (python -m bench.fetch). Not the worker image.
FROM ghcr.io/astral-sh/uv:0.6.10 AS uv

FROM python:3.13-slim
COPY --from=uv /uv /bin/uv
RUN apt-get update \
    && apt-get install --yes --no-install-recommends tesseract-ocr tesseract-ocr-fra fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/*
ENV UV_PROJECT_ENVIRONMENT=/venv UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never \
    PATH=/venv/bin:$PATH PYTHONPATH=/repo/worker/src:/repo/worker \
    PYTHONDONTWRITEBYTECODE=1 HF_HUB_OFFLINE=1 ORT_DISABLE_TELEMETRY=1
WORKDIR /build
COPY pyproject.toml uv.lock .python-version ./
RUN uv sync --frozen --group bench --no-install-project \
    && python -c "import rapidocr, onnxruntime, tokenizers"
WORKDIR /repo/worker
