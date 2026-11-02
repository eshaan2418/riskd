# riskd: common entry points. The volsurf (cgo) pricer is enabled with TAGS=volsurf (default).
TAGS ?= volsurf
JOURNAL ?= $(HOME)/quantfactory/_data/riskd/btc-20261102.jsonl.gz
SAMPLE ?= testdata/deribit-btc-sample.jsonl.gz

.PHONY: build test race bench lint fuzz demo data report replay clean

build:
	go build -tags '$(TAGS)' -o bin/riskd ./cmd/riskd

test:
	go test -tags '$(TAGS)' ./...
	go test ./...

race:
	go test -race -tags '$(TAGS)' ./...

# Pricer and engine benchmarks (synthetic books) -> bench/results/bench.json. Run on an idle machine.
bench: build
	bin/riskd bench -out bench/results/bench.json

lint:
	go vet -tags '$(TAGS)' ./...
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

fuzz:
	go test -run '^$$' -fuzz FuzzDecodeWS -fuzztime $${FUZZTIME:-30s} ./internal/deribit/

# Record a public Deribit session (no API key; public market-data channels only).
data: build
	bin/riskd record -out $(JOURNAL) -dur $${DUR:-45m}

report: build
	bin/riskd report -out research/results/report.json $(JOURNAL)

replay: build
	bin/riskd replay -out research/results/replay_mark.json $(JOURNAL)
	bin/riskd replay -vol deribit -out research/results/replay_deribit_iv.json $(JOURNAL)

# Serve the risk API over the committed sample journal at the recorded pace.
demo: build
	bin/riskd run -replay $(SAMPLE) -speed 4 -addr 127.0.0.1:8080

clean:
	rm -rf bin
