FROM golang:1.19.0

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

ADD cmd/ ./cmd
ADD internal ./internal

RUN mkdir ./bin
RUN go build -o ./bin/hl_exporter ./cmd/hl-exporter

RUN wget https://binaries.hyperliquid.xyz/Testnet/hl-visor -o /hl-visor
RUN chmod a+x /hl-visor

EXPOSE 8086

ENTRYPOINT ["/app/bin/hl_exporter"]
