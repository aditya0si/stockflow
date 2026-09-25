FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/stockflow-seed ./cmd/seed

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stockflow-api /stockflow-api
COPY --from=build /out/stockflow-seed /stockflow-seed
EXPOSE 8080
ENTRYPOINT ["/stockflow-api"]
