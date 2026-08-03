#!/usr/bin/env bash
# tracegen.sh — Generate realistic enterprise trace data for Tempo smoke testing.
#
# Uses telemetrygen (the supported replacement for the deprecated tracegen tool)
# to simulate a multi-tier e-commerce platform with realistic service names,
# operations, and span attributes.
#
# Usage:
#   ./deploy/observability/tracegen.sh
#
# Prerequisites:
#   kubectl configured and pointing at the target cluster
#   Tempo distributor running in the tempo namespace with OTLP gRPC on port 4317
#
# Each kubectl run command launches a short-lived pod that generates traces for
# 30 seconds then exits. All pods are cleaned up at the end.

set -euo pipefail

NAMESPACE="tempo"
ENDPOINT="tempo-distributor.tempo.svc.cluster.local:4317"
IMAGE="ghcr.io/open-telemetry/opentelemetry-collector-contrib/telemetrygen:latest"
DURATION="30s"
WORKERS=2

# ─── Service definitions ──────────────────────────────────────────────────────
# Each entry is: pod-name | service.name | operation
# Simulates a realistic e-commerce microservices platform.
SERVICES=(
  "api-gateway|api-gateway|HTTP POST /api/v1/orders"
  "order-service|order-service|processOrder"
  "payment-service|payment-service|authorizePayment"
  "inventory-service|inventory-service|checkStock"
  "notification-service|notification-service|sendOrderConfirmation"
  "user-service|user-service|getUserProfile"
  "product-catalog|product-catalog-service|searchProducts"
  "recommendation-engine|recommendation-engine|getRecommendations"
)

echo "Starting trace generation for ${#SERVICES[@]} services..."
echo "Endpoint: ${ENDPOINT}"
echo "Duration: ${DURATION} per service"
echo ""

PODS=()

for entry in "${SERVICES[@]}"; do
  IFS='|' read -r pod_name service_name operation <<< "$entry"

  echo "▶ Launching ${pod_name} (service.name=${service_name})"

  kubectl run "${pod_name}-tracegen" \
    --image="${IMAGE}" \
    --restart=Never \
    --namespace="${NAMESPACE}" \
    --labels="app=tracegen,service=${pod_name}" \
    -- traces \
      --otlp-endpoint="${ENDPOINT}" \
      --otlp-insecure \
      --duration="${DURATION}" \
      --workers="${WORKERS}" \
      --service="${service_name}" \
      --telemetry-attributes="deployment.environment=\"production\"" \
      --telemetry-attributes="service.version=\"1.0.0\"" \
      --telemetry-attributes="cloud.provider=\"aws\"" \
      --telemetry-attributes="cloud.region=\"us-east-2\"" \
      --telemetry-attributes="k8s.cluster.name=\"cluster2\""

  PODS+=("${pod_name}-tracegen")
done

echo ""
echo "All ${#PODS[@]} trace generators running. Waiting for completion..."
echo "Watch progress: kubectl get pods -n ${NAMESPACE} -l app=tracegen -w"
echo ""

# Wait for all pods to complete
for pod in "${PODS[@]}"; do
  echo -n "Waiting for ${pod}... "
  kubectl wait pod "${pod}" \
    --for=condition=Ready \
    --namespace="${NAMESPACE}" \
    --timeout=60s 2>/dev/null || true
  kubectl wait pod "${pod}" \
    --for=jsonpath='{.status.phase}'=Succeeded \
    --namespace="${NAMESPACE}" \
    --timeout=120s 2>/dev/null && echo "✅ done" || echo "⚠ check logs"
done

echo ""
echo "Cleaning up pods..."
for pod in "${PODS[@]}"; do
  kubectl delete pod "${pod}" --namespace="${NAMESPACE}" --ignore-not-found=true
done

echo ""
echo "Done. Open Grafana → Drilldown → Traces to explore the generated data."
echo "Services generated:"
for entry in "${SERVICES[@]}"; do
  IFS='|' read -r _ service_name _ <<< "$entry"
  echo "  • ${service_name}"
done
