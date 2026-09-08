FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /morphcache ./cmd/morphcache
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /morphcache /morphcache
EXPOSE 6380
ENTRYPOINT ["/morphcache"]
CMD ["-listen", "0.0.0.0:6380"]
