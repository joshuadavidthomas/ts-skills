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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ts-skillsd /usr/local/bin/ts-skillsd
COPY --from=build --chown=nonroot:nonroot /out/state /state
ENV TS_SKILLSD_STATE_DIR=/state
ENTRYPOINT ["/usr/local/bin/ts-skillsd"]
