FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/joshuadavidthomas/ts-skills/internal/version.Version=${VERSION}" \
    -o /out/ts-skillsd ./cmd/ts-skillsd \
    && mkdir /out/state

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 nonroot \
    && useradd --uid 65532 --gid 65532 --no-create-home --shell /usr/sbin/nologin nonroot
COPY --from=build /out/ts-skillsd /usr/local/bin/ts-skillsd
COPY --from=build --chown=nonroot:nonroot /out/state /state
ENV TS_SKILLSD_STATE_DIR=/state
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/ts-skillsd"]
