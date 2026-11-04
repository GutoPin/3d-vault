# Se construye desde la raíz del repositorio; docker-compose.yml ya lo indica.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/3dvault-server ./cmd/3dvault-server
# Las carpetas se crean aquí porque la imagen final no tiene shell. Un volumen nuevo
# hereda este dueño, de modo que el servidor no necesita ejecutarse como root.
RUN mkdir -p /out/data /out/backup && chown 65532:65532 /out/data /out/backup

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/3dvault-server /usr/local/bin/3dvault-server
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/backup /backup
ENV DATA_DIR=/data LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s CMD ["/usr/local/bin/3dvault-server", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/3dvault-server"]
CMD ["serve"]
