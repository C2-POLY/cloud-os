# Build context: the repository root (compose.yaml), so the image can embed the web front and VERSION.

FROM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# The API types shared with the mobile app (web imports ../shared).
COPY shared/ /shared/
RUN npm run build

FROM golang:1.26-alpine AS core
WORKDIR /src
COPY core/go.mod core/go.sum ./
RUN go mod download
COPY core/ ./
COPY VERSION /VERSION
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(cat /VERSION)" -o /out/core ./cmd/core

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=core /out/core /core
COPY --from=web /web/dist /srv/web
ENV STATIC_DIR=/srv/web CORE_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/core"]
