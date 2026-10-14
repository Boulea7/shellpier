# Supply an approved Go 1.26+ image pinned by digest.
ARG GO_IMAGE
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /shellpier ./cmd/shellpier

FROM scratch
COPY --from=build /shellpier /shellpier
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/shellpier", "relay", "--listen", "0.0.0.0:8080", "--token-file", "/run/secrets/relay.key"]
