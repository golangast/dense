BIN_DIR := bin
DENSE_LLM := $(BIN_DIR)/dense_llm
DENSE_TRAIN := $(BIN_DIR)/dense_train
DENSE_STUDY := $(BIN_DIR)/dense_study
DENSE_WATCH := $(BIN_DIR)/dense_watch

.PHONY: all build build-dense_llm build-dense_train build-dense_study build-dense_watch watch test fmt tidy clean help install run-dense_llm run-dense_train run-dense_study

all: build

help:
	@echo "Targets:"
	@echo "  make build               Build all binaries"
	@echo "  make build-dense_llm     Build dense_llm binary"
	@echo "  make build-dense_train   Build dense_train binary"
	@echo "  make build-dense_study   Build dense_study binary"
	@echo "  make build-dense_watch   Build dense_watch binary"
	@echo "  make watch               Build and run dense_watch with -auto-apply"
	@echo "  make test                Run tests"
	@echo "  make fmt                 Format Go sources"
	@echo "  make tidy                Run go mod tidy"
	@echo "  make clean               Remove build artifacts"
	@echo "  run-dense_llm            Run the dense_llm binary"
	@echo "  run-dense_train          Run the dense_train binary"
	@echo "  run-dense_study          Run the dense_study binary"

build: $(DENSE_LLM) $(DENSE_TRAIN) $(DENSE_STUDY) $(DENSE_WATCH)

$(DENSE_LLM):
	@mkdir -p $(BIN_DIR)
	go build -v -o $(DENSE_LLM) ./cmd/tools/dense_llm

$(DENSE_TRAIN):
	@mkdir -p $(BIN_DIR)
	go build -v -o $(DENSE_TRAIN) ./cmd/tools/dense_train

$(DENSE_STUDY):
	@mkdir -p $(BIN_DIR)
	go build -v -o $(DENSE_STUDY) ./cmd/tools/dense_study

$(DENSE_WATCH):
	@mkdir -p $(BIN_DIR)
	go build -v -o $(DENSE_WATCH) ./cmd/tools/dense_watch

build-dense_llm: $(DENSE_LLM)

build-dense_train: $(DENSE_TRAIN)

build-dense_study: $(DENSE_STUDY)

build-dense_watch: $(DENSE_WATCH)

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

watch: build-dense_watch
	./$(DENSE_WATCH) -dir=. -auto-apply

train: build-dense_train
	./$(DENSE_TRAIN)

study: build-dense_study
	./$(DENSE_STUDY)

all: build ./$(DENSE_LLM) ./$(DENSE_TRAIN) ./$(DENSE_STUDY) & rm -rf $(BIN_DIR)
	