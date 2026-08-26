SERVICES    := frontend catalog cart checkout payment inventory
VM_SERVICES := vm-web vm-api vm-client
IMAGE_REGISTRY ?= quay.io/chris_lovett/tempo-consul-microdemo
IMAGE_TAG   ?= 0.2.2
PLATFORMS   ?= linux/amd64,linux/arm64
NAMESPACE   ?= tracing-demo
RELEASE     ?= tempo-demo

.PHONY: build push build-multiarch build-vm push-vm helm-install helm-upgrade \
        helm-uninstall helm-lint helm-template verify-traces

# ─── Build (ocp-dc services) ──────────────────────────────────────────────────

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

# ─── Build (vm-dc EC2 binaries) ───────────────────────────────────────────────
# Produces linux/amd64 static binaries for deployment on the EC2 instance.
# Copy them to the VM with: scp bin/vm-* ec2-user@aws-vm-node-1:/usr/local/bin/

build-vm:
	@echo "Building vm-dc binaries (linux/amd64)"
	@mkdir -p bin
	@set -e; for svc in $(VM_SERVICES); do \
		echo "  $$svc"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
			go build -trimpath -ldflags="-s -w" \
			-o bin/$$svc ./cmd/$$svc; \
	done
	@echo "Binaries written to bin/"

push-vm: build-vm
	@echo "Copying vm-dc binaries to EC2..."
	scp bin/vm-web bin/vm-api bin/vm-client ec2-user@aws-vm-node-1:/usr/local/bin/
	@echo "Restarting services on EC2..."
	ssh ec2-user@aws-vm-node-1 \
		"sudo systemctl restart web api && sudo pkill -f vm-client 2>/dev/null; \
		 SERVICE_NAME=client PORT=9080 UPSTREAM_URI=http://localhost:9095 \
		 OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
		 nohup /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &"
	@echo "Done. Tail logs: ssh ec2-user@aws-vm-node-1 'tail -f /tmp/vm-client.log'"

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
