SERVICES    := frontend catalog cart checkout payment inventory
IMAGE_REGISTRY ?= quay.io/chris_lovett/tempo-consul-microdemo
IMAGE_TAG   ?= 0.2.0
PLATFORMS   ?= linux/amd64,linux/arm64
NAMESPACE   ?= tracing-demo
RELEASE     ?= tempo-demo

.PHONY: build push build-multiarch helm-install helm-upgrade helm-uninstall \
        helm-lint helm-template verify-traces

# ─── Build ────────────────────────────────────────────────────────────────────

build:
	@set -e; for svc in $(SERVICES); do \
		echo "Building $$svc"; \
		docker build --build-arg SERVICE=$$svc \
			-t $(IMAGE_REGISTRY)/$$svc:$(IMAGE_TAG) .; \
	done

push: build
	@set -e; for svc in $(SERVICES); do \
		echo "Pushing $$svc"; \
		docker push $(IMAGE_REGISTRY)/$$svc:$(IMAGE_TAG); \
	done

build-multiarch:
	@echo "Building multi-arch images ($(PLATFORMS))"
	@set -e; for svc in $(SERVICES); do \
		echo "  $$svc"; \
		docker buildx build --platform $(PLATFORMS) \
			--build-arg SERVICE=$$svc \
			-t $(IMAGE_REGISTRY)/$$svc:$(IMAGE_TAG) \
			--push .; \
	done

# ─── Helm ─────────────────────────────────────────────────────────────────────

helm-lint:
	helm lint charts/tempo-consul-microdemo/

helm-template:
	helm template $(RELEASE) charts/tempo-consul-microdemo/ \
		--namespace $(NAMESPACE) \
		--debug

helm-install:
	kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	helm install $(RELEASE) charts/tempo-consul-microdemo/ \
		--namespace $(NAMESPACE) \
		--values charts/tempo-consul-microdemo/values.yaml \
		--wait

helm-upgrade:
	helm upgrade $(RELEASE) charts/tempo-consul-microdemo/ \
		--namespace $(NAMESPACE) \
		--values charts/tempo-consul-microdemo/values.yaml \
		--wait

helm-uninstall:
	helm uninstall $(RELEASE) --namespace $(NAMESPACE)

# ─── Verification ─────────────────────────────────────────────────────────────

verify-traces:
	@echo "Checking OTel Collector health..."
	kubectl exec -n $(NAMESPACE) deploy/otel-collector -- \
		wget -qO- http://localhost:13133/ | head -5
	@echo ""
	@echo "Run load test to generate traces, then query Tempo:"
	@echo "  kubectl port-forward -n tempo svc/tempo-query-frontend 3200:3200"
	@echo "  curl 'http://localhost:3200/api/traces?service=frontend&limit=5'"
