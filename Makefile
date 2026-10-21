IMAGE ?= slincsilver/real-time-chat-microservice
VERSION ?= 1.0.1
PLATFORMS ?= linux/amd64,linux/arm64
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.9.0
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct -- go.mod go.sum cmd internal pkg Dockerfile .dockerignore)
BUILD_ARGS = --build-arg SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH)
COSIGN_IDENTITY ?= santiagolhgpro@gmail.com
COSIGN_ISSUER ?= https://accounts.google.com
DIGEST = $(shell docker buildx imagetools inspect $(IMAGE):$(VERSION) --format '{{json .Manifest.Digest}}' | tr -d '"')

.PHONY: test e2e e2e-short e2e-image perf bench pgo scan docker-build docker-push sign verify

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

# Refreshes the profile-guided optimization profile from the perf suite's
# CPU profiles; go build picks up cmd/server/default.pgo automatically.
pgo:
	E2E_PERF_LABEL=pgo go test -tags e2e -count=1 -run TestPerformance ./test/e2e/
	go tool pprof -proto test/e2e/reports/pgo-cpu-*.pb.gz > cmd/server/default.pgo

# Fails on any vulnerability reachable from our code, and on any fixable
# critical or high vulnerability anywhere in the image.
scan: docker-build
	go run $(GOVULNCHECK) ./...
	docker scout cves --exit-code --only-fixed --only-severity critical,high local://$(IMAGE):$(VERSION)

docker-build:
	docker buildx build $(BUILD_ARGS) --load -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-push: scan
	docker buildx build $(BUILD_ARGS) --platform $(PLATFORMS) --sbom=true --provenance=mode=max \
		--output type=registry,\"name=$(IMAGE):$(VERSION),$(IMAGE):latest\",rewrite-timestamp=true .

# Keyless Sigstore signature of the pushed multi-arch index; opens a browser
# to authenticate the signer.
sign:
	cosign sign --yes $(IMAGE)@$(DIGEST)

verify:
	cosign verify --certificate-identity=$(COSIGN_IDENTITY) --certificate-oidc-issuer=$(COSIGN_ISSUER) $(IMAGE):$(VERSION) > /dev/null
