# Python OpenTelemetry Instrumentation Guide

This guide enables Python applications running on **Consul Service Mesh** to emit distributed traces to **Grafana Tempo** via the **OpenTelemetry SDK**.

## Overview

Python applications in your Consul mesh will:
1. Initialize the OpenTelemetry SDK with OTLP/gRPC exporter
2. Instrument HTTP servers and clients to propagate W3C TraceContext headers
3. Emit spans to the OTel Collector (which forwards to Tempo)
4. Leverage Envoy sidecars for automatic context propagation across service boundaries

## Prerequisites

- Python 3.8+
- OpenTelemetry Python SDK and instrumentations installed
- OTel Collector running in your cluster (deployed via Helm alongside Tempo)
- Application packaged in a container with Consul sidecar injection enabled

## Installation

### 1. Add Dependencies to `requirements.txt`

```txt
# Core OpenTelemetry SDK
opentelemetry-api>=1.20.0
opentelemetry-sdk>=1.20.0

# Protocol and exporters
opentelemetry-exporter-otlp>=0.41b0
opentelemetry-exporter-otlp-proto-grpc>=0.41b0

# Instrumentations (auto-instrument HTTP, requests, etc.)
opentelemetry-instrumentation>=0.41b0
opentelemetry-instrumentation-flask>=0.41b0          # if using Flask
opentelemetry-instrumentation-fastapi>=0.41b0        # if using FastAPI
opentelemetry-instrumentation-requests>=0.41b0       # if using requests library
opentelemetry-instrumentation-urllib3>=0.41b0        # if using urllib3

# Semantic conventions
opentelemetry-semantic-conventions>=0.41b0
```

Or install all at once:
```bash
pip install opentelemetry-api opentelemetry-sdk opentelemetry-exporter-otlp \
  opentelemetry-instrumentation-flask opentelemetry-instrumentation-requests
```

## Configuration

### 2. Initialize the Tracer (Minimal Setup)

Create a shared `tracing.py` module:

```python
import os
import logging
from typing import Callable, Optional
from contextlib import contextmanager

from opentelemetry import trace, context
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import SERVICE_NAME, Resource
from opentelemetry.propagate import set_global_textmap
from opentelemetry.propagators.w3c_trace_context import W3CTraceContextPropagator
from opentelemetry.propagators.baggage import BaggagePropagator
from opentelemetry.propagate import composite_propagator

logger = logging.getLogger(__name__)


def init_tracing(service_name: Optional[str] = None) -> trace.Tracer:
    """
    Initialize the OpenTelemetry SDK for distributed tracing.
    
    Args:
        service_name: Name of the service (defaults to SERVICE_NAME env var or service name)
    
    Returns:
        A Tracer instance for creating spans
    
    Environment variables:
        SERVICE_NAME: Override the service name
        OTEL_EXPORTER_OTLP_ENDPOINT: OTel Collector gRPC endpoint (default: otel-collector:4317)
        SAMPLE_RATE: Trace sampling ratio 0.0-1.0 (default: 1.0)
    """
    
    if not service_name:
        service_name = os.getenv("SERVICE_NAME", "python-app")
    
    otel_endpoint = os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")
    sample_rate = float(os.getenv("SAMPLE_RATE", "1.0"))
    
    # Create OTLP/gRPC exporter
    otlp_exporter = OTLPSpanExporter(
        endpoint=otel_endpoint,
        insecure=True,  # Set to False and configure certs for production mTLS
    )
    
    # Create and configure TracerProvider
    trace_provider = TracerProvider(
        resource=Resource.create({SERVICE_NAME: service_name}),
        active_span_processor=None,  # Disable active span processor for batching
    )
    
    # Add batch span processor for efficiency
    trace_provider.add_span_processor(BatchSpanProcessor(otlp_exporter))
    
    # Set the global TracerProvider
    trace.set_tracer_provider(trace_provider)
    
    # Set global text map propagators for W3C TraceContext + Baggage
    # This ensures traceparent headers are injected/extracted on all HTTP calls
    set_global_textmap(
        composite_propagator.CompositeHTTPPropagator([
            W3CTraceContextPropagator(),
            BaggagePropagator(),
        ])
    )
    
    logger.info(
        f"OTel tracer initialized (service={service_name}, "
        f"collector={otel_endpoint}, sampleRate={sample_rate})"
    )
    
    return trace.get_tracer(service_name)


def get_tracer(service_name: Optional[str] = None) -> trace.Tracer:
    """Get or create the global tracer."""
    return trace.get_tracer(service_name or os.getenv("SERVICE_NAME", "python-app"))


# Cleanup function for graceful shutdown
def shutdown_tracing():
    """Flush all pending spans and shutdown the tracer provider."""
    tracer_provider = trace.get_tracer_provider()
    if hasattr(tracer_provider, "force_flush"):
        tracer_provider.force_flush(timeout_millis=5000)
    if hasattr(tracer_provider, "shutdown"):
        tracer_provider.shutdown()
```

### 3. Instrument a Flask Application

Flask example with automatic and manual instrumentation:

```python
from flask import Flask, request, jsonify
import requests
import logging

from tracing import init_tracing, shutdown_tracing, get_tracer
from opentelemetry.instrumentation.flask import FlaskInstrumentor
from opentelemetry.instrumentation.requests import RequestsInstrumentor
from opentelemetry import trace
from opentelemetry.trace import Status, StatusCode

logger = logging.getLogger(__name__)
app = Flask(__name__)

# Initialize OTel and auto-instrument Flask + requests
init_tracing(service_name="checkout")
FlaskInstrumentor().instrument_app(app)
RequestsInstrumentor().instrument()


@app.route("/checkout", methods=["POST"])
def checkout():
    """Checkout endpoint that calls downstream services."""
    tracer = get_tracer()
    
    # Get the current span (auto-created by FlaskInstrumentor)
    span = trace.get_current_span()
    
    user_id = request.json.get("user_id", "unknown")
    
    # Add custom attributes to the span
    span.set_attribute("user.id", user_id)
    span.set_attribute("checkout.status", "started")
    
    # Log structured message (will include traceID for log correlation)
    logger.info(f"Checkout started for user={user_id}, traceID={span.get_span_context().trace_id}")
    
    try:
        # Call another service — headers auto-propagate via RequestsInstrumentor
        cart_response = requests.get(
            "http://cart:8082/cart/user123",
            headers={"User-Agent": "checkout-service"}
        )
        cart_response.raise_for_status()
        cart_data = cart_response.json()
        
        span.set_attribute("cart.total", cart_data.get("total", 0))
        span.set_attribute("item.count", len(cart_data.get("items", [])))
        
        # Process payment
        payment_response = requests.post(
            "http://payment:8084/authorize",
            json={"order_id": "ord-123", "amount": cart_data.get("total", 0)},
        )
        payment_response.raise_for_status()
        payment_data = payment_response.json()
        
        span.set_attribute("payment.status", payment_data.get("status"))
        
        return jsonify({
            "order_id": "ord-123",
            "status": "authorized",
            "total": cart_data.get("total", 0),
        }), 200
        
    except Exception as e:
        # Mark span as errored
        span.record_exception(e)
        span.set_status(Status(StatusCode.ERROR, str(e)))
        logger.error(f"Checkout failed: {str(e)}", exc_info=True)
        return jsonify({"error": "checkout failed"}), 500


@app.before_request
def before_request():
    """Optional: Log all incoming requests with trace context."""
    span = trace.get_current_span()
    logger.debug(f"Request: {request.method} {request.path}, traceID={span.get_span_context().trace_id}")


@app.teardown_appcontext
def teardown(exc=None):
    """Cleanup on shutdown."""
    if exc:
        logger.error(f"Shutdown error: {exc}")


if __name__ == "__main__":
    try:
        app.run(host="0.0.0.0", port=8083, debug=False)
    finally:
        shutdown_tracing()
```

### 4. Instrument a FastAPI Application

FastAPI example (recommended for new projects):

```python
from fastapi import FastAPI, Request, HTTPException
from fastapi.responses import JSONResponse
import httpx
import logging
import os

from tracing import init_tracing, shutdown_tracing, get_tracer
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry import trace
from opentelemetry.trace import Status, StatusCode

logger = logging.getLogger(__name__)
app = FastAPI(title="checkout-service")

# Initialize OTel early
init_tracing(service_name="checkout")

# Auto-instrument FastAPI
FastAPIInstrumentor.instrument_app(app)

# For httpx (async HTTP client), add this before defining routes:
from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor
HTTPXClientInstrumentor().instrument()


@app.post("/checkout")
async def checkout(request: Request):
    """Checkout endpoint with distributed tracing."""
    tracer = get_tracer()
    span = trace.get_current_span()
    
    body = await request.json()
    user_id = body.get("user_id", "unknown")
    
    # Add custom attributes
    span.set_attribute("user.id", user_id)
    span.set_attribute("operation", "checkout")
    
    logger.info(f"Checkout initiated for user={user_id}")
    
    try:
        # Use async httpx client (instrumented automatically)
        async with httpx.AsyncClient() as client:
            # Fetch cart — W3C traceparent header injected automatically
            cart_url = os.getenv("CART_URL", "http://cart:8082")
            cart_response = await client.get(f"{cart_url}/cart/{user_id}")
            cart_response.raise_for_status()
            cart_data = cart_response.json()
            
            span.set_attribute("cart.total", cart_data.get("total", 0))
            
            # Process payment
            payment_url = os.getenv("PAYMENT_URL", "http://payment:8084")
            payment_response = await client.post(
                f"{payment_url}/authorize",
                json={"order_id": "ord-123", "amount": cart_data.get("total")},
            )
            payment_response.raise_for_status()
            payment_data = payment_response.json()
            
            span.set_attribute("payment.status", payment_data.get("status"))
            
            return {
                "order_id": "ord-123",
                "status": payment_data.get("status"),
                "total": cart_data.get("total"),
            }
    
    except Exception as e:
        span.record_exception(e)
        span.set_status(Status(StatusCode.ERROR, str(e)))
        logger.error(f"Checkout failed: {str(e)}", exc_info=True)
        raise HTTPException(status_code=500, detail="Checkout failed")


@app.get("/health")
async def health():
    """Health check endpoint."""
    return {"status": "ok", "service": "checkout"}


@app.on_event("shutdown")
async def on_shutdown():
    """Cleanup spans on shutdown."""
    shutdown_tracing()


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8083)
```

### 5. Manual Span Creation

For fine-grained control, create spans manually:

```python
from opentelemetry import trace

tracer = trace.get_tracer(__name__)

def process_order(order_id: str, amount: float):
    """Example of manual span creation."""
    
    # Start a new span
    with tracer.start_as_current_span("process_order") as span:
        span.set_attribute("order.id", order_id)
        span.set_attribute("order.amount", amount)
        
        # Nested span
        with tracer.start_as_current_span("validate_order") as validate_span:
            if amount <= 0:
                validate_span.set_attribute("validation.passed", False)
                raise ValueError("Invalid amount")
            validate_span.set_attribute("validation.passed", True)
        
        # Another nested span
        with tracer.start_as_current_span("reserve_inventory") as reserve_span:
            # Do inventory work
            reserve_span.set_attribute("inventory.reserved", 5)
        
        # Process completes; span auto-closes on exiting the context
        return {"order_id": order_id, "status": "processed"}
```

### 6. Structured Logging with Trace Context

For log correlation with Grafana Loki (via Alloy), emit logs with embedded trace IDs:

```python
import logging
from opentelemetry import trace

# Configure logging format to include trace context
class TraceContextFilter(logging.Filter):
    def filter(self, record):
        span = trace.get_current_span()
        ctx = span.get_span_context()
        record.traceID = ctx.trace_id
        record.spanID = ctx.span_id
        return True

# Set up logger
logger = logging.getLogger(__name__)
handler = logging.StreamHandler()

# Format: level=... traceID=... spanID=... msg="..."
formatter = logging.Formatter(
    "level=%(levelname)s traceID=%(traceID)x spanID=%(spanID)x msg=%(message)s"
)
handler.setFormatter(formatter)
logger.addHandler(handler)
logger.addFilter(TraceContextFilter())

# Use in code
logger.info(f"Processing order {order_id}")  
# Output: level=INFO traceID=4bf92f3577b34da6a3ce929d0e0e4736 spanID=00f067aa0ba902b7 msg="Processing order ord-123"
```

When Grafana Alloy tails logs with this format and Loki has `derivedFields` configured, clicking the `traceID` value opens the corresponding Tempo trace.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `SERVICE_NAME` | `python-app` | Service name (appears in Tempo) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `otel-collector:4317` | OTel Collector gRPC endpoint |
| `SAMPLE_RATE` | `1.0` | Trace sampling ratio (0.0–1.0); use <1.0 for high-volume services |

## Kubernetes Deployment (Helm)

When deploying to Consul Service Mesh, ensure your Helm chart includes:

```yaml
apiVersion: v1
kind: Pod
metadata:
  annotations:
    consul.hashicorp.com/connect-inject: "true"
    consul.hashicorp.com/transparent-proxy: "true"
    consul.hashicorp.com/connect-service-upstreams: "cart:8082,payment:8084,inventory:8085"
spec:
  containers:
  - name: checkout
    image: myregistry/checkout:latest
    ports:
    - containerPort: 8083
      name: http
    env:
    - name: SERVICE_NAME
      value: checkout
    - name: PORT
      value: "8083"
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: "otel-collector:4317"
    - name: SAMPLE_RATE
      value: "1.0"
    - name: CART_URL
      value: "http://cart:8082"
    - name: PAYMENT_URL
      value: "http://payment:8084"
    - name: INVENTORY_URL
      value: "http://inventory:8085"
```

### Key Points

1. **Consul Sidecar Injection**: The `consul.hashicorp.com/connect-inject: "true"` annotation injects an Envoy sidecar that:
   - Routes traffic through the service mesh
   - Automatically propagates W3C TraceContext headers
   - Enables mTLS between services
   - Creates Envoy proxy-level spans visible in Tempo

2. **Service Upstreams**: The `connect-service-upstreams` annotation tells Consul which services this app calls, enabling service intentions.

3. **OTel Collector**: Must be running in the same namespace and accessible via `otel-collector:4317`.

## Common Patterns

### Pattern 1: Timeout with Span Status

```python
from opentelemetry.trace import Status, StatusCode
import asyncio

async def call_with_timeout(url: str, timeout_seconds: float):
    tracer = trace.get_tracer(__name__)
    
    with tracer.start_as_current_span("http_request") as span:
        span.set_attribute("http.url", url)
        span.set_attribute("http.timeout_seconds", timeout_seconds)
        
        try:
            async with httpx.AsyncClient() as client:
                response = await asyncio.wait_for(
                    client.get(url),
                    timeout=timeout_seconds
                )
                span.set_attribute("http.status_code", response.status_code)
                return response
        except asyncio.TimeoutError as e:
            span.set_status(Status(StatusCode.ERROR, f"Timeout after {timeout_seconds}s"))
            raise
```

### Pattern 2: Retry with Span Events

```python
def call_with_retry(url: str, max_retries: int = 3):
    tracer = trace.get_tracer(__name__)
    
    with tracer.start_as_current_span("http_request_with_retry") as span:
        span.set_attribute("http.url", url)
        
        for attempt in range(1, max_retries + 1):
            try:
                response = requests.get(url, timeout=5)
                response.raise_for_status()
                span.add_event("request_succeeded", {"attempt": attempt})
                return response
            except requests.exceptions.RequestException as e:
                span.add_event("request_failed", {
                    "attempt": attempt,
                    "error": str(e)
                })
                if attempt == max_retries:
                    span.record_exception(e)
                    span.set_status(Status(StatusCode.ERROR, f"Failed after {max_retries} retries"))
                    raise
```

### Pattern 3: Custom Attributes from Request Context

```python
from flask import request, g

@app.before_request
def before_request():
    """Capture request context in span."""
    span = trace.get_current_span()
    span.set_attribute("http.method", request.method)
    span.set_attribute("http.url", request.url)
    span.set_attribute("http.client_ip", request.remote_addr)
    
    # Store in Flask's g object for use in handlers
    g.span = span

@app.route("/checkout", methods=["POST"])
def checkout():
    span = g.span
    user_id = request.json.get("user_id")
    span.set_attribute("user.id", user_id)
    # ... rest of handler
```

## Verification

### 1. Check That Traces Are Reaching Tempo

```bash
# Port-forward Tempo query frontend
kubectl port-forward -n tempo svc/tempo-query-frontend 3200:3200 &

# Generate a trace by calling your service
curl -X POST http://checkout:8083/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"test-user"}'

# Query Tempo directly
curl 'http://localhost:3200/api/search?service.name=checkout&limit=5' | jq .
```

### 2. View Traces in Grafana

1. Open **Grafana → Explore**
2. Select **Tempo** datasource
3. Set query type to **Search**
4. Service: `checkout` (or your service name)
5. Click **Run query**

### 3. Test W3C TraceContext Propagation

Add logging to your service:

```python
logger.info(f"Incoming traceparent header: {request.headers.get('traceparent')}")
```

You should see the same trace ID across all services in the waterfall view.

## Troubleshooting

### No Traces Appearing

**Check OTel Collector health:**
```bash
kubectl exec -n tracing-demo deploy/otel-collector -- wget -qO- http://localhost:13133/
```

**Check collector logs:**
```bash
kubectl logs -n tracing-demo deploy/otel-collector --tail=50 | grep -i "error\|refused"
```

**Verify your app can reach the collector:**
```bash
# Inside your pod
nslookup otel-collector
```

### Traces Missing Spans from Downstream Services

**Verify W3C TraceContext propagation:**
- Add logging to confirm `traceparent` headers are being sent
- Check that requests library or httpx is instrumented (`RequestsInstrumentor().instrument()` or `HTTPXClientInstrumentor().instrument()`)

**Check Consul service mesh:**
```bash
# Verify Envoy sidecar is running
kubectl get pods -n tracing-demo -o wide
# Each app pod should have 2 containers (app + envoy)
```

### High Latency in Traces

**Check OTel Collector metrics:**
```bash
# Port-forward and check Prometheus metrics
kubectl port-forward -n tracing-demo svc/otel-collector 8888:8888 &
curl http://localhost:8888/metrics | grep "otelcol_exporter\|otelcol_receiver"
```

**Reduce sample rate if needed:**
```bash
# In your deployment
env:
  - name: SAMPLE_RATE
    value: "0.1"  # Sample 10% of traces
```

## Best Practices

1. **Always initialize OTel early in your app startup**, before creating routes or handlers
2. **Use auto-instrumentation** (FlaskInstrumentor, FastAPIInstrumentor, RequestsInstrumentor) first — manual spans only for custom logic
3. **Set meaningful attributes** on spans (`user.id`, `order.id`, `product.id`) for filtering in Tempo
4. **Use span events** for logging important milestones (e.g., `"payment_authorized"`, `"inventory_reserved"`)
5. **Avoid sampling on critical flows** (leave `SAMPLE_RATE=1.0`); use ratio-based sampling only for high-volume services
6. **Test log → trace correlation** by emitting structured logs with trace ID and verifying Grafana's derived fields work

## References

- [OpenTelemetry Python Docs](https://opentelemetry.io/docs/instrumentation/python/)
- [OpenTelemetry Instrumentations](https://opentelemetry-python-contrib.readthedocs.io/)
- [W3C Trace Context](https://www.w3.org/TR/trace-context/)
- [Grafana Tempo Documentation](https://grafana.com/docs/tempo/latest/)
- [Consul Service Mesh Integration](https://www.consul.io/docs/connect)
