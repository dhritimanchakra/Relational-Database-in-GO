# syntax=docker/dockerfile:1

# ---- build: static binary, no cgo -------------------------------------------
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/reldb .

# ---- test (opt-in): docker build --target test . ----------------------------
FROM build AS test
RUN go vet ./... && go test -count=1 ./...

# ---- runtime: just the binary -----------------------------------------------
FROM scratch
COPY --from=build /out/reldb /reldb
WORKDIR /data
VOLUME ["/data"]
ENTRYPOINT ["/reldb", "-db", "/data/reldb.data"]
