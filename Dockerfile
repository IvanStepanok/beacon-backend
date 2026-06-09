# ---- build: fully static, no CGO ----
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server
# Empty photo dir, owned by nonroot, so a fresh named volume mounted here is writable.
RUN mkdir -p /photos

# ---- run: distroless, nonroot ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
COPY --from=build --chown=65532:65532 /photos /data/photos
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
