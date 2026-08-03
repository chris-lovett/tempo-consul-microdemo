package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"

	"github.com/chris-lovett/tempo-consul-microdemo/pkg/httpx"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/models"
	"github.com/chris-lovett/tempo-consul-microdemo/pkg/tracing"
)

func main() {
	serviceName := getenv("SERVICE_NAME", "checkout")
	port := getenv("PORT", "8083")
	cartURL := getenv("CART_URL", "http://localhost:8082")
	inventoryURL := getenv("INVENTORY_URL", "http://localhost:8085")
	paymentURL := getenv("PAYMENT_URL", "http://localhost:8084")
	log.SetPrefix("[" + serviceName + "] ")

	tracer, cleanup := tracing.Init(serviceName)
	defer cleanup()

	client := httpx.NewClient(tracer)

	r := mux.NewRouter()
	r.Use(tracing.Middleware(tracer))

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.HealthResponse{Status: "ok", Service: serviceName})
	}).Methods(http.MethodGet)

	r.HandleFunc("/checkout", checkoutHandler(client, cartURL, inventoryURL, paymentURL)).Methods(http.MethodPost)

	addr := ":" + port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func checkoutHandler(client *http.Client, cartURL, inventoryURL, paymentURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		span := tracing.SpanFromContext(r)

		var req models.CheckoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		tracing.Tag(span, "user.id", req.UserID)

		ctx := r.Context()

		// 1. Fetch cart.
		cart, err := fetchCart(ctx, client, cartURL, req.UserID)
		if err != nil {
			tracing.SetError(span, "cart fetch failed: "+err.Error())
			http.Error(w, `{"error":"failed to fetch cart"}`, http.StatusInternalServerError)
			return
		}
		if len(cart.Items) == 0 {
			http.Error(w, `{"error":"cart is empty"}`, http.StatusBadRequest)
			return
		}
		tracing.Tag(span, "item.count", fmt.Sprintf("%d", len(cart.Items)))
		tracing.Tag(span, "cart.total", fmt.Sprintf("%.2f", cart.Total))

		// 2. Reserve inventory for each item.
		for _, item := range cart.Items {
			resReq := models.InventoryReservation{ProductID: item.ProductID, Quantity: item.Quantity}
			invResp, err := reserveInventory(ctx, client, inventoryURL, resReq)
			if err != nil || !invResp.Success {
				msg := "inventory reservation failed"
				if err != nil {
					msg = err.Error()
				} else {
					msg = invResp.Message
				}
				tracing.SetError(span, msg)
				http.Error(w, fmt.Sprintf(`{"error":"%s"}`, msg), http.StatusConflict)
				return
			}
		}

		// 3. Process payment.
		orderID := generateOrderID()
		tracing.Tag(span, "order.id", orderID)

		payReq := models.PaymentRequest{
			OrderID: orderID,
			Amount:  cart.Total,
			UserID:  req.UserID,
		}
		payResp, err := processPayment(ctx, client, paymentURL, payReq)
		if err != nil {
			tracing.SetError(span, "payment failed: "+err.Error())
			http.Error(w, `{"error":"payment service unavailable"}`, http.StatusBadGateway)
			return
		}
		tracing.Tag(span, "payment.status", payResp.Status)

		// 4. Clear cart on successful payment.
		if payResp.Status == "authorized" {
			_ = deleteCart(ctx, client, cartURL, req.UserID)
		}

		w.Header().Set("Content-Type", "application/json")
		if payResp.Status != "authorized" {
			w.WriteHeader(http.StatusPaymentRequired)
		}
		_ = json.NewEncoder(w).Encode(models.CheckoutResponse{
			OrderID:       orderID,
			UserID:        req.UserID,
			Total:         cart.Total,
			PaymentStatus: payResp.Status,
			Items:         cart.Items,
		})
	}
}

func fetchCart(ctx context.Context, client *http.Client, cartURL, userID string) (*models.Cart, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cartURL+"/cart/"+userID, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cart models.Cart
	if err := json.NewDecoder(resp.Body).Decode(&cart); err != nil {
		return nil, err
	}
	return &cart, nil
}

func reserveInventory(ctx context.Context, client *http.Client, inventoryURL string, res models.InventoryReservation) (*models.InventoryResponse, error) {
	body, _ := json.Marshal(res)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, inventoryURL+"/reserve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var invResp models.InventoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&invResp); err != nil {
		return nil, err
	}
	return &invResp, nil
}

func processPayment(ctx context.Context, client *http.Client, paymentURL string, payReq models.PaymentRequest) (*models.PaymentResponse, error) {
	body, _ := json.Marshal(payReq)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, paymentURL+"/authorize", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payResp models.PaymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&payResp); err != nil {
		return nil, err
	}
	return &payResp, nil
}

func deleteCart(ctx context.Context, client *http.Client, cartURL, userID string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, cartURL+"/cart/"+userID, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func generateOrderID() string {
	b := make([]byte, 6)
	for i := range b {
		b[i] = byte('a' + (i*7+os.Getpid())%26)
	}
	return fmt.Sprintf("ord-%x", b)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
