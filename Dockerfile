FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY app/web ./app/web
RUN CGO_ENABLED=0 go build -o /out/proxylite ./cmd/proxylite

FROM debian:bookworm-slim
WORKDIR /app
ENV TZ=Asia/Shanghai
# HTTPS proxy sources and GeoIP updates need the system trust store at runtime.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/proxylite /usr/local/bin/proxylite
COPY app/web ./app/web
EXPOSE 8899 18080 18081 18082 18083 18084 18085 18086 18087 18088 18089
CMD ["proxylite"]
