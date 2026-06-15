# ---- build: cgo enabled (uber/h3-go binds the H3 C library) ----
# golang:1.26 is debian-based and ships gcc, so cgo builds in-image. The resulting
# binary is dynamically linked against glibc, so the run stage uses distroless/base
# (glibc) rather than distroless/static.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server
# Empty photo dir, owned by nonroot, so a fresh named volume mounted here is writable.
RUN mkdir -p /photos

# ---- run: distroless (glibc), nonroot ----
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/server /server
COPY --from=build --chown=65532:65532 /photos /data/photos
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
