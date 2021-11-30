FROM golang:1.17-buster as build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY tools/transfer_metadata.go ./
RUN go build -o /out/transfer_metadata

FROM gcr.io/distroless/base
COPY --from=build /out/transfer_metadata /transfer_metadata
ENTRYPOINT ["/transfer_metadata"]
