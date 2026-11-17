FROM ghcr.io/astral-sh/uv:0.6.10 AS uv

FROM python:3.13-slim
COPY --from=uv /uv /bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
COPY pyproject.toml uv.lock .python-version ./
RUN uv sync --frozen --no-dev --no-install-project
COPY src ./src
RUN uv sync --frozen --no-dev
USER nobody
CMD ["/app/.venv/bin/python", "-m", "worker"]
