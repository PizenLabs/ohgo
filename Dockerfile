# Static ohgo binary, no cgo anywhere in the dependency graph.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/ohgo ./cmd/ohgo

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ohgo /ohgo
WORKDIR /work
ENTRYPOINT ["/ohgo"]
