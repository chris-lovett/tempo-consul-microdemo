# Observability Stack — Quick-start deploy order
#
# This directory contains all Helm values and Kubernetes manifests needed to
# run the full observability stack alongside the demo application.
#
# Prerequisites:
#   - Helm 3.x
#   - kubectl configured for your cluster
#   - Grafana Helm repo: helm repo add grafana https://grafana.github.io/helm-charts
#   - Prometheus community repo: helm repo add prometheus-community https://prometheus-community.github.io/helm-charts

## 1 — Deploy Grafana Tempo (microservices mode)
```
kubectl create namespace tempo
helm install tempo grafana/tempo-distributed \
  --namespace tempo \
  --values tempo-values.yaml
```

## 2 — Deploy the application (includes the OTel Collector)
```
kubectl create namespace tracing-demo
helm install tempo-demo ../charts/tempo-consul-microdemo/ \
  --namespace tracing-demo \
  --values ../charts/tempo-consul-microdemo/values.yaml
```

## 3 — Add Tempo as a Grafana data source
```
# If using Grafana Operator:
kubectl apply -f grafana-tempo-datasource.yaml -n observability

# If using the Grafana community Helm chart, add to your grafana values:
# grafana.additionalDataSources: (see grafana-tempo-datasource.yaml for jsonData)
```

## 4 — Merge Consul observability values
```
helm upgrade consul hashicorp/consul -n consul \
  -f <your-existing-consul-values.yaml> \
  -f consul-values-observability.yaml
```

## Verification

```bash
# Port-forward Tempo query frontend
kubectl port-forward -n tempo svc/tempo-query-frontend 3200:3200 &

# Generate a few traces
FRONTEND=$(kubectl get route frontend -n tracing-demo -o jsonpath='{.spec.host}')
curl http://${FRONTEND}/products
curl -X POST http://${FRONTEND}/cart/demo/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":1}'
curl -X POST http://${FRONTEND}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo"}'

# Query Tempo directly
curl 'http://localhost:3200/api/search?service.name=frontend&limit=5' | jq .
```
