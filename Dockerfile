FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/proxylite ./cmd/proxylite

FROM debian:bookworm-slim
WORKDIR /app
ENV TZ=Asia/Shanghai
COPY --from=build /out/proxylite /usr/local/bin/proxylite
COPY app/web ./app/web
EXPOSE 8899 18080 18081 18082 18083 18084 18085 18086 18087 18088 18089
CMD ["proxylite"]
