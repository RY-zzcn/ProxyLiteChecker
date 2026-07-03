FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/proxylite ./cmd/proxylite

FROM debian:bookworm-slim
WORKDIR /app
COPY --from=build /out/proxylite /usr/local/bin/proxylite
COPY app/web ./app/web
EXPOSE 8899 18080
CMD ["proxylite"]
