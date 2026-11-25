# OpenFGA Explorer: a static binary on a minimal, non-root image.
#
#   docker build -t openfga-explorer .
#   docker run --rm -p 127.0.0.1:8090:8090 -e OPENFGA_API_URL=http://host.docker.internal:8080 openfga-explorer
#
# Inside a container the explorer listens on every interface (that is the
# container's own network); choose what to expose when publishing the port
# (127.0.0.1:8090:8090 keeps it on this machine). It has no authentication of
# its own: expose it beyond this machine only behind an authenticating proxy.

FROM golang:1.27.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fgaexplorer ./cmd/fgaexplorer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fgaexplorer /fgaexplorer
COPY explorer.example.yaml /etc/fgaexplorer/explorer.yaml
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/fgaexplorer", "serve", "--config", "/etc/fgaexplorer/explorer.yaml", "--listen", "0.0.0.0:8090"]
