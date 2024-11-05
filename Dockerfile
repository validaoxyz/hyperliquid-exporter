FROM golang:1.19.0

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

ADD cmd/ ./cmd
ADD internal ./internal

RUN mkdir ./bin
RUN go build -o ./bin/hl_exporter ./cmd/hl-exporter

EXPOSE 8086

CMD ["/app/bin/hl_exporter"]
