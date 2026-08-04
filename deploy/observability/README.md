# Observability Stack — Setup Guide

This directory contains everything needed to deploy the full observability stack
on OpenShift and wire it into this demo, starting from scratch.

**Target environment:**
- OpenShift 4.12+ cluster with Consul Service Mesh installed
- `grafana` namespace used for Grafana
- `prometheus` namespace used for Prometheus
- `tempo` namespace used for Grafana Tempo
- Adjust these namespaces as needed if your cluster uses different names

---

## Files in this directory

| File | Purpose |
|---|---|
| `grafana-values.yaml` | Helm values for deploying standalone Grafana on OpenShift |
| `grafana-route.yaml` | OpenShift Route to expose the Grafana UI externally |
| `prometheus-values.yaml` | Helm values for deploying standalone Prometheus (kube-prometheus-stack) |
| `tempo-values.yaml` | Helm values for deploying Grafana Tempo (microservices mode, S3 backend) |
| `grafana-tempo-datasource.yaml` | ConfigMap that provisions the Tempo datasource into Grafana |
| `grafana-loki-datasource.yaml` | ConfigMap that provisions the Loki datasource into Grafana |
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

### 0b — Create the Prometheus namespace

```bash
kubectl create namespace prometheus
```

### 0c — Deploy Prometheus (kube-prometheus-stack)

The kube-prometheus-stack bundles Prometheus, the Prometheus Operator, and
Alertmanager. The values file disables components already provided by the
OpenShift platform stack (kube-state-metrics, node-exporter, control-plane
scrapers) to avoid conflicts.

```bash
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace prometheus \
  --values prometheus-values.yaml

# Wait for the operator and Prometheus pods to be ready
kubectl get pods -n prometheus -w
```

Key settings in [`prometheus-values.yaml`](prometheus-values.yaml):
- `serviceMonitorNamespaceSelector: {}` and `serviceMonitorSelector: {}` — Prometheus watches **all namespaces** for ServiceMonitors, including `tracing-demo`
- `grafana.enabled: false` — Grafana is deployed separately below
- `kubeStateMetrics.enabled: false` and `nodeExporter.enabled: false` — avoid duplication with OpenShift platform monitoring

### 0d — Deploy Grafana

```bash
helm install grafana grafana/grafana \
  --namespace grafana \
  --values grafana-values.yaml

# Wait for the Grafana pod to be ready
kubectl get pods -n grafana -l app.kubernetes.io/name=grafana -w
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
kubectl apply -f grafana-route.yaml -n grafana
```

Get the Grafana URL:

```bash
oc get route grafana -n grafana -o jsonpath='{.spec.host}'
# Open: https://<output>
# Default credentials: admin / changeme  (set in grafana-values.yaml — change before prod use)
```

### 0f — Add the Prometheus datasource to Grafana

The Prometheus Operator exposes a Prometheus service inside the cluster.
Add it as a datasource in Grafana:

1. Open Grafana → **Administration → Data sources → Add new datasource**
2. Select **Prometheus**
3. URL: `http://prometheus-kube-prometheus-prometheus.prometheus.svc.cluster.local:9090`
4. Click **Save & test**
5. Copy the datasource **UID** from the URL bar — you will need it in Step 3 below
   (it looks like `ae3f1234-...`, or may simply be `prometheus`)

### 0g — Deploy Loki and register it in Grafana

```bash
kubectl create namespace loki
helm install loki grafana/loki-distributed \
  --namespace loki \
  --values loki-values.yaml
kubectl apply -f grafana-loki-datasource.yaml -n grafana
```

This provisions Loki in its own namespace and registers it with Grafana as a datasource named `Loki`.

Then confirm log volume in Grafana:

1. Open Grafana → **Explore** → select the **Loki** datasource
2. Run a query such as:

```logql
sum(rate({namespace="tracing-demo"}[5m]))
```

This returns the log line ingestion rate for the demo application namespace. To inspect trace-linked logs, open **Explore** in Grafana and click the `traceID` link on a log line after selecting the `Loki` datasource.

> If the Grafana sidecar is disabled, add `grafana-loki-datasource.yaml` as an additional datasource in your Grafana Helm values instead.

---

## Step 1 — Find your Grafana and Prometheus details

Before applying anything, collect the values you'll need to substitute below:

```bash
# Find your Grafana and Prometheus Helm releases and namespaces
helm list -A | grep -E 'grafana|prometheus'

# Example output:
#   grafana      grafana      grafana/grafana      ...
#   prometheus   prometheus   prometheus-community/kube-prometheus-stack ...

# Check whether the Grafana sidecar datasource loader is enabled
helm get values <grafana-release> -n <grafana-namespace> | grep -A5 sidecar

# Find your Prometheus datasource UID in Grafana:
#   Grafana → Administration → Data sources → Prometheus → copy the UID from the URL
#   It looks like: ae3f1234-...  (often just "prometheus" on default installs)
```

---

## Step 1b — Set up AWS S3 for Tempo storage

Tempo requires object storage for trace persistence. Complete this before
deploying Tempo in Step 2.

### Create the S3 bucket

```bash
# Replace placeholders with your values
export BUCKET_NAME=tempo-traces-myorg-prod
export REGION=us-east-1

aws s3api create-bucket \
  --bucket ${BUCKET_NAME} \
  --region ${REGION} \
  --create-bucket-configuration LocationConstraint=${REGION}

# Block all public access
aws s3api put-public-access-block \
  --bucket ${BUCKET_NAME} \
  --public-access-block-configuration \
    "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"
```

### Required IAM permissions

Tempo needs these S3 actions on the bucket:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "s3:PutObject",
      "s3:GetObject",
      "s3:DeleteObject",
      "s3:ListBucket"
    ],
    "Resource": [
      "arn:aws:s3:::${BUCKET_NAME}",
      "arn:aws:s3:::${BUCKET_NAME}/*"
    ]
  }]
}
```

### Option A — IRSA (recommended for ROSA / EKS)

IRSA lets Tempo pods assume an IAM role without storing any credentials in the
cluster. This is the preferred approach.

```bash
# 1. Create the IAM policy
aws iam create-policy \
  --policy-name TempoS3Policy \
  --policy-document file://tempo-iam-policy.json   # save the JSON above to this file

# 2. Create an IAM role with a trust policy for the Tempo service accounts.
#    On ROSA, use the rosa CLI or the AWS console to create the role and
#    associate it with the cluster's OIDC provider:
#      Principal: arn:aws:iam::<account-id>:oidc-provider/<oidc-provider-url>
#      Condition StringEquals:
#        <oidc-provider-url>:sub:
#          - system:serviceaccount:tempo:tempo-distributor
#          - system:serviceaccount:tempo:tempo-ingester
#          - system:serviceaccount:tempo:tempo-querier
#          - system:serviceaccount:tempo:tempo-compactor

# 3. Attach the policy to the role
aws iam attach-role-policy \
  --role-name TempoS3Role \
  --policy-arn arn:aws:iam::<account-id>:policy/TempoS3Policy

# 4. Annotate each Tempo service account with the role ARN.
#    Add this to tempo-values.yaml under each component, e.g.:
#
#      distributor:
#        serviceAccount:
#          annotations:
#            eks.amazonaws.com/role-arn: arn:aws:iam::<account-id>:role/TempoS3Role
#
#    (repeat for ingester, querier, compactor)
```

With IRSA configured, leave `access_key` and `secret_key` **unset** in
`tempo-values.yaml` — the AWS SDK picks up the role automatically.

### Option B — Static credentials via Kubernetes Secret

Use this only if IRSA is not available on your cluster.

```bash
# Create the secret — do NOT commit credentials to git
kubectl create secret generic tempo-s3-credentials \
  --from-literal=AWS_ACCESS_KEY_ID=<your-access-key-id> \
  --from-literal=AWS_SECRET_ACCESS_KEY=<your-secret-access-key> \
  --namespace tempo
```

Then in `tempo-values.yaml`:
1. Set `access_key: ${AWS_ACCESS_KEY_ID}` and `secret_key: ${AWS_SECRET_ACCESS_KEY}` under `storage.trace.s3`
2. Uncomment the four `extraEnvFrom` blocks in the file to mount the Secret into each Tempo component

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
1. Set `metadata.namespace` to your Grafana namespace (e.g. `grafana`)
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
