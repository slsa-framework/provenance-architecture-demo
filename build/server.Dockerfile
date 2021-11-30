FROM golang:1.17-buster as build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/*.go ./
RUN go build -o /out/server

FROM gcr.io/distroless/base
COPY --from=build /out/server /server
ENTRYPOINT ["/server"]
