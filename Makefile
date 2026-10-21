IMAGE ?= slincsilver/real-time-chat-microservice
VERSION ?= 1.0.0
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: test e2e e2e-short e2e-image perf bench docker-build docker-push

test:
	go test -race ./...

e2e:
	go test -tags e2e -count=1 ./test/e2e/

e2e-short:
	go test -tags e2e -count=1 -short ./test/e2e/

e2e-image: docker-build
	E2E_IMAGE=$(IMAGE):$(VERSION) go test -tags e2e -count=1 -short ./test/e2e/

perf:
	go test -tags e2e -count=1 -run TestPerformance -v ./test/e2e/

bench:
	go test -run=^$$ -bench=. -benchmem ./...

docker-build:
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-push:
	docker buildx build --platform $(PLATFORMS) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .
