export FRONTEND_URL=https://frontend-tracing-demo.apps.rosa.cluster2.6cxo.p3.openshiftapps.com
export TEMPO_URL=http://localhost:3200
export USER=demo-user-42

# 1. Add item to cart
curl -s -X POST $FRONTEND_URL/cart/$USER/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":2}'

# 2. Checkout
curl -s -X POST $FRONTEND_URL/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"'$USER'"}'

# 3. Get the trace ID from the frontend log
oc logs -n tracing-demo deployment/frontend -c frontend --tail=3 | grep checkout



# Step 1 — add item to cart
curl -s -X POST $FRONTEND_URL/cart/$USER/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":2}'

# Step 2 — checkout (frontend → checkout → cart + inventory + payment)
curl -s -X POST $FRONTEND_URL/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"'$USER'"}'


# 20 checkouts so the waterfall table has enough rows to sort and filter during the demo.

  for i in $(seq 1 20); do
  curl -s -X POST $FRONTEND_URL/cart/$USER/items \
    -H "Content-Type: application/json" \
    -d '{"product_id":"prod-1","quantity":1}' > /dev/null
  curl -s -X POST $FRONTEND_URL/checkout \
    -H "Content-Type: application/json" \
    -d '{"user_id":"'$USER'"}' > /dev/null
  sleep 1
done
echo "Burst complete."

# error testing 


# Inject 100% failure (Tab 3 port-forward must be running)
curl -s -X POST $PAYMENT_ADMIN/admin/config \
  -H "Content-Type: application/json" \
  -d '{"failure_rate":1.0,"latency_ms":0}' | jq .
# Add item + checkout — response will show payment_status: "declined"
curl -s -X POST $FRONTEND_URL/cart/$USER/items \
  -H "Content-Type: application/json" \
  -d '{"product_id":"prod-1","quantity":1}' > /dev/null

curl -s -X POST $FRONTEND_URL/checkout \
  -H "Content-Type: application/json" \
  -d '{"user_id":"'$USER'"}'
# Capture the error trace ID
export ERROR_TRACE_ID=$(oc logs -n $NS deployment/frontend -c frontend --tail=2 \
  | grep checkout \
  | awk '{for(i=1;i<=NF;i++) if($i~/^traceID=/) print $i}' \
  | tail -1 | cut -d= -f2)
echo "Error trace ID: $ERROR_TRACE_ID"