# syntax=docker/dockerfile:1
#
# Base images are pinned by immutable digest so a rebuild cannot silently pick
# up a changed tag. The readable tag is kept alongside the digest; refresh a pin
# deliberately with:
#   docker buildx imagetools inspect <tag>
#
# node:22-alpine
ARG NODE_IMAGE=node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402
# golang:1.25-alpine
ARG GOLANG_IMAGE=golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59
# gcr.io/distroless/static-debian12:nonroot
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM ${NODE_IMAGE} AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM ${GOLANG_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-seed ./cmd/seed
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-reconcile ./cmd/reconcile

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/stockflow-api /stockflow-api
COPY --from=build /out/stockflow-seed /stockflow-seed
COPY --from=build /out/stockflow-reconcile /stockflow-reconcile
EXPOSE 8080
# Distroless has no shell or HTTP client; the binary probes its own /healthz.
HEALTHCHECK --interval=5s --timeout=3s --start-period=10s --retries=20 \
  CMD ["/stockflow-api", "healthcheck"]
ENTRYPOINT ["/stockflow-api"]
