FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/birdlist ./cmd/birdlist

FROM alpine:3.22

COPY --from=build /out/birdlist /usr/local/bin/birdlist
# app.NewServer resolves templates relative to its compiled source path.
COPY templates /templates

ENV BIRD_LIST_DB=/data/birds.db
ENV BIRD_LIST_TEMPLATE_DIR=/templates

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/birdlist"]
CMD ["-port", "8080"]
