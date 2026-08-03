# Observability Stack — Setup Guide

This directory contains everything needed to deploy the full observability stack
on OpenShift and wire it into this demo, starting from scratch.

**Target environment:**
- OpenShift 4.12+ cluster with Consul Service Mesh installed
- `monitoring` namespace used for Grafana and Prometheus (adjust as needed)

---

## Files in this directory

| File | Purpose |
|---|---|
| `grafana-values.yaml` | Helm values for deploying standalone Grafana on OpenShift |
| `grafana-route.yaml` | OpenShift Route to expose the Grafana UI externally |
| `prometheus-values.yaml` | Helm values for deploying standalone Prometheus (kube-prometheus-stack) |
| `tempo-values.yaml` | Helm values for deploying Grafana Tempo (microservices mode, S3 backend) |
| `grafana-tempo-datasource.yaml` | ConfigMap that provisions the Tempo datasource into Grafana |
| `servicemonitor-otel-collector.yaml` | Tells Prometheus to scrape the OTel Collector |
| `consul-values-observability.yaml` | Consul Helm overlay — metrics + Grafana deep-links from Consul UI |

---

## Step 0 — Deploy standalone Grafana and Prometheus

> **Skip this step if Grafana and Prometheus are already running in your cluster.**
> Jump straight to [Step 1](#step-1--find-your-grafana-and-prometheus-details).

### 0a — Add Helm repos

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
```

### 0b — Create the monitoring namespace

```bash
kubectl create namespace monitoring
```

### 0c — Deploy Prometheus (kube-prometheus-stack)

The kube-prometheus-stack bundles Prometheus, the Prometheus Operator, and
Alertmanager. The values file disables components already provided by the
OpenShift platform stack (kube-state-metrics, node-exporter, control-plane
scrapers) to avoid conflicts.

```bash
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --values prometheus-values.yaml

# Wait for the operator and Prometheus pods to be ready
kubectl get pods -n monitoring -w
```

Key settings in [`prometheus-values.yaml`](prometheus-values.yaml):
- `serviceMonitorNamespaceSelector: {}` and `serviceMonitorSelector: {}` — Prometheus watches **all namespaces** for ServiceMonitors, including `tracing-demo`
- `grafana.enabled: false` — Grafana is deployed separately below
- `kubeStateMetrics.enabled: false` and `nodeExporter.enabled: false` — avoid duplication with OpenShift platform monitoring

### 0d — Deploy Grafana

```bash
helm install grafana grafana/grafana \
  --namespace monitoring \
  --values grafana-values.yaml

# Wait for the Grafana pod to be ready
kubectl get pods -n monitoring -l app.kubernetes.io/name=grafana -w
```

Key settings in [`grafana-values.yaml`](grafana-values.yaml):
- `sidecar.datasources.enabled: true` — watches for ConfigMaps labelled `grafana_datasource=1` and loads them as datasources with no restart
- `sidecar.dashboards.enabled: true` — same pattern for dashboards
- `persistence.enabled: true` — retains dashboards and settings across restarts
- Security context configured for OpenShift non-root requirements

### 0e — Expose Grafana via an OpenShift Route

The Grafana Helm chart does not create OpenShift Routes natively. Apply the
provided Route manifest:

```bash
kubectl apply -f grafana-route.yaml -n monitoring
```

Get the Grafana URL:

```bash
oc get route grafana -n monitoring -o jsonpath='{.spec.host}'
# Open: https://<output>
# Default credentials: admin / changeme  (set in grafana-values.yaml — change before prod use)
```

### 0f — Add the Prometheus datasource to Grafana

The Prometheus Operator exposes a Prometheus service inside the cluster.
Add it as a datasource in Grafana:

1. Open Grafana → **Administration → Data sources → Add new datasource**
2. Select **Prometheus**
3. URL: `http://prometheus-kube-prometheus-prometheus.monitoring.svc.cluster.local:9090`
4. Click **Save & test**
5. Copy the datasource **UID** from the URL bar — you will need it in Step 3 below
   (it looks like `ae3f1234-...`, or may simply be `prometheus`)

---

## Step 1 — Find your Grafana and Prometheus details

Before applying anything, collect the values you'll need to substitute below:

```bash
# Find your Grafana and Prometheus Helm releases and namespaces
helm list -A | grep -E 'grafana|prometheus'

# Example output:
#   grafana      monitoring   grafana/grafana      ...
#   prometheus   monitoring   prometheus-community/kube-prometheus-stack ...

# Check whether the Grafana sidecar datasource loader is enabled
helm get values <grafana-release> -n <grafana-namespace> | grep -A5 sidecar

# Find your Prometheus datasource UID in Grafana:
#   Grafana → Administration → Data sources → Prometheus → copy the UID from the URL
#   It looks like: ae3f1234-...  (often just "prometheus" on default installs)
```

---

## Step 2 — Deploy Grafana Tempo

Edit `tempo-values.yaml` first — set your S3 bucket, endpoint, and region (or switch to GCS/MinIO).

```bash
helm repo add grafana https://grafana.github.io/helm-charts && helm repo update
kubectl create namespace tempo
helm install tempo grafana/tempo-distributed \
  --namespace tempo \
  --values tempo-values.yaml

# Verify all Tempo pods reach Running state before continuing
kubectl get pods -n tempo -w
```

---

## Step 3 — Register Tempo as a Grafana datasource

Edit `grafana-tempo-datasource.yaml`:
1. Set `metadata.namespace` to your Grafana namespace (e.g. `monitoring`)
2. Set `datasourceUid: prometheus` to match the actual UID of your existing Prometheus datasource

```bash
kubectl apply -f grafana-tempo-datasource.yaml -n <grafana-namespace>

# Confirm Grafana picked it up (sidecar reloads within ~30s, no restart needed)
# Navigate to: Grafana → Administration → Data sources → verify "Tempo" appears
```

> **If the sidecar is not enabled** on your Grafana, add the datasource under
> `grafana.additionalDataSources` in your Grafana Helm values instead — the
> exact block is in the commented Option B section at the bottom of the file.

---

## Step 4 — Deploy the application (includes OTel Collector)

```bash
kubectl create namespace tracing-demo
helm install tempo-demo ../charts/tempo-consul-microdemo/ \
  --namespace tracing-demo \
  --values ../charts/tempo-consul-microdemo/values.yaml

kubectl get pods -n tracing-demo -w
```

---

## Step 5 — Wire Prometheus scraping of the OTel Collector

This enables the service graph panel in Grafana.

Edit `servicemonitor-otel-collector.yaml`: add the label your Prometheus
`serviceMonitorSelector` requires. For the kube-prometheus-stack default, that
label is `release: <prometheus-release-name>`.

```bash
# Check your Prometheus serviceMonitorSelector
helm get values <prometheus-release> -n <prometheus-namespace> \
  | grep -A5 serviceMonitorSelector

# Apply the ServiceMonitor
kubectl apply -f servicemonitor-otel-collector.yaml -n tracing-demo

# Confirm Prometheus is scraping it (allow ~1 minute for discovery)
# Navigate to: Prometheus UI → Status → Targets → filter by "otel-collector"
```

---

## Step 6 — Consul UI deep-links (optional)

Update the `dashboardURLTemplates.service` URL in `consul-values-observability.yaml`
to point at your Grafana instance, then:

```bash
helm upgrade consul hashicorp/consul -n consul \
  -f <your-existing-consul-values.yaml> \
  -f consul-values-observability.yaml
```

---

## Verification

```bash
# Port-forward Tempo query frontend
kubectl port-forward -n tempo svc/tempo-query-frontend 3200:3200 &

# Generate traces
FRONTEND=$(oc get route frontend -n tracing-demo -o jsonpath='{.spec.host}')
curl https://${FRONTEND}/products
curl -sX POST https://${FRONTEND}/cart/demo/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":1}'
curl -sX POST https://${FRONTEND}/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo"}'

# Query Tempo directly to confirm spans are arriving
curl 'http://localhost:3200/api/search?service.name=frontend&limit=5' | jq .

# Then open Grafana → Explore → select Tempo datasource → Run query
```
