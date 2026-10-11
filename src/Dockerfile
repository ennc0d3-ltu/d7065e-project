# One image recipe for every Go service: docker build --build-arg SERVICE=<cmd dir> .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SERVICE
RUN test -n "$SERVICE" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}
# The detector ships the model it was evaluated with: training is deterministic
# (fixed seed, fixed training runs), so the image holds exactly that model.
RUN mkdir -p /out/models && if [ "$SERVICE" = "detector" ]; then \
      go run ./cmd/trainer -out /out/models/detector.json; fi

FROM alpine:3.20
RUN adduser -D -u 10001 app && mkdir -p /data/outbox /data/registry && chown app /data/outbox /data/registry
# outbox files live here; mount a volume on it so a recreated container keeps them (D13)
ENV OUTBOX_DIR=/data/outbox
USER app
COPY --from=build /out/service /service
COPY --from=build /out/models /models
ENTRYPOINT ["/service"]
