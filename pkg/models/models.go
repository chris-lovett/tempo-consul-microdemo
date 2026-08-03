package models

// Product represents an item in the product catalog.
type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
}

// CartItem represents a single line-item in a shopping cart.
type CartItem struct {
	ProductID string  `json:"product_id"`
	Quantity  int     `json:"quantity"`
	Price     float64 `json:"price"`
	Name      string  `json:"name"`
}

// Cart holds all items for a specific user.
type Cart struct {
	UserID string     `json:"user_id"`
	Items  []CartItem `json:"items"`
	Total  float64    `json:"total"`
}

// CheckoutRequest is the request body for initiating a checkout.
type CheckoutRequest struct {
	UserID string `json:"user_id"`
}

// CheckoutResponse summarises the result of a completed checkout.
type CheckoutResponse struct {
	OrderID       string     `json:"order_id"`
	UserID        string     `json:"user_id"`
	Total         float64    `json:"total"`
	PaymentStatus string     `json:"payment_status"`
	Items         []CartItem `json:"items"`
}

// PaymentRequest is sent to the payment service.
type PaymentRequest struct {
	OrderID string  `json:"order_id"`
	Amount  float64 `json:"amount"`
	UserID  string  `json:"user_id"`
}

// PaymentResponse is returned by the payment service.
type PaymentResponse struct {
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}

// InventoryReservation is sent to the inventory service to hold stock.
type InventoryReservation struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// InventoryResponse is returned by the inventory service.
type InventoryResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// HealthResponse is the standard health-check payload.
type HealthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}
