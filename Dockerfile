FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-seed ./cmd/seed
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-reconcile ./cmd/reconcile

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stockflow-api /stockflow-api
COPY --from=build /out/stockflow-seed /stockflow-seed
COPY --from=build /out/stockflow-reconcile /stockflow-reconcile
EXPOSE 8080
ENTRYPOINT ["/stockflow-api"]
