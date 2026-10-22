FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /snugkv ./cmd/snugkv
# Secrets reader for other images: COPY --from=<this image> /snugkv-secrets /usr/local/bin/
RUN CGO_ENABLED=0 go build -trimpath -o /snugkv-secrets ./cmd/snugkv-secrets
# Server monitoring agent: docker run --entrypoint /snug-agent (see docs/AGENT.md)
RUN CGO_ENABLED=0 go build -trimpath -o /snug-agent ./cmd/snug-agent
RUN mkdir /data
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /snugkv /snugkv
COPY --from=build /snugkv-secrets /snugkv-secrets
COPY --from=build /snug-agent /snug-agent
# Data directory owned by the nonroot user, so a fresh named volume is writable.
COPY --from=build --chown=65532:65532 /data /data
VOLUME /data
EXPOSE 6380
ENTRYPOINT ["/snugkv"]
CMD ["-listen", "0.0.0.0:6380"]
