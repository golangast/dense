BIN_DIR := bin
DENSE_LLM := $(BIN_DIR)/dense_llm
DENSE_TRAIN := $(BIN_DIR)/dense_train

.PHONY: all build build-dense_llm build-dense_train test fmt tidy clean help install run-dense_llm run-dense_train

all: build

help:
	@echo "Targets:"
	@echo "  make build               Build all binaries"
	@echo "  make build-dense_llm     Build dense_llm binary"
	@echo "  make build-dense_train   Build dense_train binary"
	@echo "  make test                Run tests"
	@echo "  make fmt                 Format Go sources"
	@echo "  make tidy                Run go mod tidy"
	@echo "  make clean               Remove build artifacts"
	@echo "  run-dense_llm            Run the dense_llm binary"
	@echo "  run-dense_train          Run the dense_train binary"

build: $(DENSE_LLM) $(DENSE_TRAIN)

$(DENSE_LLM):
	@mkdir -p $(BIN_DIR)
	go build -v -o $(DENSE_LLM) ./cmd/tools/dense_llm

$(DENSE_TRAIN):
	@mkdir -p $(BIN_DIR)
	go build -v -o $(DENSE_TRAIN) ./cmd/tools/dense_train

build-dense_llm: $(DENSE_LLM)

build-dense_train: $(DENSE_TRAIN)

test:
	go test ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)

install:
	go install ./cmd/tools/...

llm: build-dense_llm
	./$(DENSE_LLM)

train: build-dense_train
	./$(DENSE_TRAIN)

