FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/rsshubalance ./cmd/rsshubalance \
    && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rsshubalance /rsshubalance
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV RSSHUBALANCE_STATE_FILE=/data/instances.json
VOLUME /data
EXPOSE 1200
ENTRYPOINT ["/rsshubalance"]
