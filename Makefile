.PHONY: build test bench docker docker-run

build:
	CGO_ENABLED=0 go build -o reldb .

test:
	go vet ./...
	go test -count=1 -race ./...

# Fixed iteration count: the file backend is append-only, so letting Go pick
# a huge N would write gigabytes.
bench:
	go test -run '^$$' -bench . -benchmem -benchtime=100000x

docker:
	docker build -t reldb .

# Data lives in ./data on the host (bind mount) and survives container restarts.
docker-run:
	mkdir -p data
	docker run --rm -it -v "$(CURDIR)/data:/data" reldb
