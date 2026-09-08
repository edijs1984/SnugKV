FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /snugkv ./cmd/snugkv
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /snugkv /snugkv
EXPOSE 6380
ENTRYPOINT ["/snugkv"]
CMD ["-listen", "0.0.0.0:6380"]
