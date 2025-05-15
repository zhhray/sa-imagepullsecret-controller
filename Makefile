APP_NAME ?= sa-imagepullsecret-controller
IMAGE_TAG ?= v4.0.0
REGISTRY ?= build-harbor.alauda.cn

.PHONY: build
build:
	@echo "Building binary..."
	CGO_ENABLED=0 GOOS=linux go build -o bin/controller ./cmd/controller

.PHONY: docker-build
docker-build: build
	@echo "Building Docker image..."
	docker build -t $(REGISTRY)/acp/$(APP_NAME):$(IMAGE_TAG) .

.PHONY: deploy
deploy:
	@echo "Deploying to cluster..."
	kubectl apply -f config/rbac.yaml
	kubectl apply -f config/deployment.yaml

.PHONY: test
test:
	go test -v ./...

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/
