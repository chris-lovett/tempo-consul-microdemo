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
#
# Usage — build only (outputs to bin/):
#   make build-vm
#
# Usage — build and deploy (requires EC2_HOST to be set):
#   EC2_HOST=<public-ip-or-hostname> make push-vm
#   EC2_HOST=ec2-user@1.2.3.4       make push-vm   # explicit user
#
# Alternative — run deploy/ec2/install.sh directly on EC2 (no SCP needed):
#   ssh ec2-user@<host> "cd /path/to/repo && bash deploy/ec2/install.sh"

EC2_HOST ?= $(error EC2_HOST is not set. Usage: EC2_HOST=<ip-or-host> make push-vm)
EC2_USER ?= ec2-user

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
	@echo "Copying vm-dc binaries to $(EC2_USER)@$(EC2_HOST)..."
	scp bin/vm-web bin/vm-api bin/vm-client $(EC2_USER)@$(EC2_HOST):/usr/local/bin/
	@echo "Restarting services on EC2..."
	ssh $(EC2_USER)@$(EC2_HOST) \
		"sudo systemctl restart web api && sudo pkill -f vm-client 2>/dev/null; sleep 1; \
		 SERVICE_NAME=client PORT=9080 UPSTREAM_URI=http://localhost:9095 \
		 OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 \
		 nohup /usr/local/bin/vm-client > /tmp/vm-client.log 2>&1 &"
	@echo "Done. Tail logs: ssh $(EC2_USER)@$(EC2_HOST) 'tail -f /tmp/vm-client.log'"

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
