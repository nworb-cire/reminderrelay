package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	haclient "github.com/mkelcik/go-ha-client/v2"

	"github.com/njoerd114/reminderrelay/internal/model"
)

// RESTClient is the subset of [haclient.Client] methods used by the adapter.
// Defining it as an interface allows mock injection in tests.
type RESTClient interface {
	Ping(ctx context.Context) error
	// CallService POSTs to /api/services/<domain>/<service> without
	// return_response. Used for mutations (add, update, remove).
	CallService(ctx context.Context, domain, service string, body io.Reader) error
	// CallServiceWithResponse POSTs with ?return_response=true. Used for
	// todo.get_items which returns data.
	CallServiceWithResponse(ctx context.Context, domain, service string, body io.Reader) (haclient.ServiceCallResponse, error)
}

// haClientWrapper wraps [haclient.Client] and adds a plain CallService method
// that POSTs without ?return_response — required for HA services that don't
// support responses (e.g. todo.add_item, todo.update_item, todo.remove_item).
type haClientWrapper struct {
	client  *haclient.Client
	baseURL string
	token   string
	hc      *http.Client
}

func (w *haClientWrapper) Ping(ctx context.Context) error {
	return w.client.Ping(ctx)
}

// CallService POSTs the body to /api/services/<domain>/<service> without
// appending ?return_response, so HA does not try to return data.
func (w *haClientWrapper) CallService(ctx context.Context, domain, service string, body io.Reader) error {
	endpoint := fmt.Sprintf("%s/api/services/%s/%s",
		strings.TrimRight(w.baseURL, "/"),
		url.PathEscape(domain),
		url.PathEscape(service),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("create service request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.hc.Do(req)
	if err != nil {
		return fmt.Errorf("execute service request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusBadRequest {
		var br struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&br)
		return errors.New(br.Message)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("HA returned 401 Unauthorized — check ha_token")
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HA returned unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (w *haClientWrapper) CallServiceWithResponse(ctx context.Context, domain, service string, body io.Reader) (haclient.ServiceCallResponse, error) {
	return w.client.CallServiceWithResponse(ctx, domain, service, body)
}

// Adapter provides sync-engine–oriented operations on Home Assistant todo
// lists via the REST and WebSocket APIs. Create one with [NewAdapter] or
// [NewAdapterWithClient].
type Adapter struct {
	rest    RESTClient
	baseURL string
	wsURL   string
	token   string
	logger  *slog.Logger
}

// NewAdapter creates an Adapter backed by HA REST and the native todo item
// WebSocket subscription API.
func NewAdapter(haURL, token string, logger *slog.Logger) (*Adapter, error) {
	rest, err := haclient.NewClient(haURL,
		haclient.WithToken(token),
		haclient.WithLogger(logger),
	)
	if err != nil {
		return nil, fmt.Errorf("create HA REST client: %w", err)
	}

	wrapper := &haClientWrapper{
		client:  rest,
		baseURL: haURL,
		token:   token,
		hc:      &http.Client{},
	}

	wsURL, err := websocketURL(haURL)
	if err != nil {
		return nil, err
	}
	return &Adapter{rest: wrapper, baseURL: haURL, wsURL: wsURL, token: token, logger: logger}, nil
}

// NewAdapterWithClient creates an Adapter with a caller-supplied REST client.
// Intended for testing with a mock [RESTClient]. WebSocket features
// (SubscribeChanges) are unavailable on adapters created this way.
func NewAdapterWithClient(rest RESTClient, logger *slog.Logger) *Adapter {
	return &Adapter{rest: rest, logger: logger}
}

// Ping validates the HA connection and token with retry.
func (a *Adapter) Ping(ctx context.Context) error {
	err := Retry(ctx, defaultMaxAttempts, func() error {
		return a.rest.Ping(ctx)
	})
	if err != nil {
		return fmt.Errorf("ping HA: %w", err)
	}
	return nil
}

// ValidateTodoEntities ensures mapped HA entities can preserve every field
// ReminderRelay projects. Failing early avoids silently dropping due times or
// metadata on an integration that only implements a subset of TodoItem.
func (a *Adapter) ValidateTodoEntities(ctx context.Context, entityIDs []string) error {
	const required = 1 | 2 | 4 | 16 | 32 | 64
	for _, entityID := range entityIDs {
		endpoint := strings.TrimRight(a.baseURL, "/") + "/api/states/" + url.PathEscape(entityID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("validate %s: %w", entityID, err)
		}
		req.Header.Set("Authorization", "Bearer "+a.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("validate %s: %w", entityID, err)
		}
		var state struct {
			Attributes struct {
				SupportedFeatures int `json:"supported_features"`
			} `json:"attributes"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&state)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("mapped HA todo entity %s returned HTTP %d", entityID, resp.StatusCode)
		}
		if decodeErr != nil {
			return fmt.Errorf("decode state for %s: %w", entityID, decodeErr)
		}
		if state.Attributes.SupportedFeatures&required != required {
			return fmt.Errorf("mapped HA todo entity %s lacks required CRUD, due-date/time, or description support (supported_features=%d)", entityID, state.Attributes.SupportedFeatures)
		}
	}
	return nil
}

// Connect verifies WebSocket authentication. SubscribeChanges owns the
// long-lived connection and reconnect loop.
func (a *Adapter) Connect(ctx context.Context) error {
	if a.wsURL == "" {
		return fmt.Errorf("WebSocket client not configured")
	}
	conn, err := a.dialAndAuthenticate(ctx)
	if err != nil {
		return err
	}
	return conn.Close()
}

// Close is retained for the Engine connector interface. The subscription
// connection is context-owned and closes itself on cancellation.
func (a *Adapter) Close() error {
	return nil
}

// GetItems fetches all todo items for the given HA entity.
func (a *Adapter) GetItems(ctx context.Context, entityID string) ([]model.Item, error) {
	data := buildGetItemsData(entityID)

	var resp haclient.ServiceCallResponse
	err := Retry(ctx, defaultMaxAttempts, func() error {
		var callErr error
		resp, callErr = a.rest.CallServiceWithResponse(ctx, domainTodo, serviceGetItems, serviceBody(data))
		return callErr
	})
	if err != nil {
		return nil, fmt.Errorf("get items for %s: %w", entityID, err)
	}

	return parseGetItemsResponse(resp, entityID)
}

// AddItem creates a new todo item in the given HA entity. The item's Priority
// is encoded as a description prefix automatically.
func (a *Adapter) AddItem(ctx context.Context, entityID string, item *model.Item) error {
	data := buildAddItemData(entityID, item)
	err := Retry(ctx, defaultMaxAttempts, func() error {
		return a.rest.CallService(ctx, domainTodo, serviceAddItem, serviceBody(data))
	})
	if err != nil {
		return fmt.Errorf("add item %q to %s: %w", item.Title, entityID, err)
	}
	return nil
}

// UpdateItem updates an existing todo item in HA by stable item UID.
func (a *Adapter) UpdateItem(ctx context.Context, entityID, identifier string, item *model.Item) error {
	data := buildUpdateItemData(entityID, identifier, item)
	err := Retry(ctx, defaultMaxAttempts, func() error {
		return a.rest.CallService(ctx, domainTodo, serviceUpdateItem, serviceBody(data))
	})
	if err != nil {
		return fmt.Errorf("update item %q in %s: %w", identifier, entityID, err)
	}
	return nil
}

// RemoveItem deletes a todo item from HA by stable item UID.
func (a *Adapter) RemoveItem(ctx context.Context, entityID, identifier string) error {
	data := buildRemoveItemData(entityID, identifier)
	err := Retry(ctx, defaultMaxAttempts, func() error {
		return a.rest.CallService(ctx, domainTodo, serviceRemoveItem, serviceBody(data))
	})
	if err != nil {
		return fmt.Errorf("remove item %q from %s: %w", identifier, entityID, err)
	}
	return nil
}

// SubscribeChanges uses Home Assistant's dedicated todo/item/subscribe
// WebSocket command. Unlike state_changed, this fires for item edits that do
// not change the entity's incomplete-count state (for example title, due time,
// tags in the metadata block, or assignment).
func (a *Adapter) SubscribeChanges(ctx context.Context, entityIDs []string, callback func(entityID string)) error {
	if a.wsURL == "" {
		return fmt.Errorf("WebSocket client not configured")
	}
	backoff := time.Second
	for {
		err := a.subscribeTodoItemsOnce(ctx, entityIDs, callback)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.logger.Warn("HA todo subscription disconnected; reconnecting", "error", err, "backoff", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

type wsMessage struct {
	ID      int             `json:"id,omitempty"`
	Type    string          `json:"type"`
	Success bool            `json:"success,omitempty"`
	Message string          `json:"message,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func websocketURL(haURL string) (string, error) {
	parsed, err := url.Parse(haURL)
	if err != nil {
		return "", fmt.Errorf("parse HA URL for WebSocket: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("HA URL scheme %q cannot be used for WebSocket", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/websocket"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (a *Adapter) dialAndAuthenticate(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, a.wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial HA WebSocket: %w", err)
	}
	fail := func(err error) (*websocket.Conn, error) {
		_ = conn.Close()
		return nil, err
	}

	var message wsMessage
	if err := conn.ReadJSON(&message); err != nil {
		return fail(fmt.Errorf("read HA authentication challenge: %w", err))
	}
	if message.Type != "auth_required" {
		return fail(fmt.Errorf("unexpected HA WebSocket greeting %q", message.Type))
	}
	if err := conn.WriteJSON(map[string]interface{}{
		"type":         "auth",
		"access_token": a.token,
	}); err != nil {
		return fail(fmt.Errorf("send HA WebSocket authentication: %w", err))
	}
	if err := conn.ReadJSON(&message); err != nil {
		return fail(fmt.Errorf("read HA WebSocket authentication result: %w", err))
	}
	if message.Type != "auth_ok" {
		return fail(fmt.Errorf("HA WebSocket authentication failed: %s", message.Message))
	}
	return conn, nil
}

func (a *Adapter) subscribeTodoItemsOnce(ctx context.Context, entityIDs []string, callback func(string)) error {
	conn, err := a.dialAndAuthenticate(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	stopCloser := make(chan struct{})
	defer close(stopCloser)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopCloser:
		}
	}()

	entityByID := make(map[int]string, len(entityIDs))
	for i, entityID := range entityIDs {
		id := i + 1
		entityByID[id] = entityID
		if err := conn.WriteJSON(map[string]interface{}{
			"id":        id,
			"type":      "todo/item/subscribe",
			"entity_id": entityID,
		}); err != nil {
			return fmt.Errorf("subscribe to %s: %w", entityID, err)
		}
	}

	for {
		var message wsMessage
		if err := conn.ReadJSON(&message); err != nil {
			return err
		}
		entityID, tracked := entityByID[message.ID]
		if !tracked {
			continue
		}
		switch message.Type {
		case "result":
			if !message.Success {
				return fmt.Errorf("HA rejected todo subscription for %s: %s", entityID, message.Error)
			}
		case "event":
			callback(entityID)
		}
	}
}

// serviceBody marshals data to a JSON [io.Reader] for service calls.
func serviceBody(data map[string]interface{}) io.Reader {
	b, _ := json.Marshal(data) //nolint:errcheck // map[string]interface{} always marshals
	return bytes.NewReader(b)
}

// parseGetItemsResponse extracts todo items from the service call response.
func parseGetItemsResponse(resp haclient.ServiceCallResponse, entityID string) ([]model.Item, error) {
	raw, ok := resp.ServiceResponse[entityID]
	if !ok {
		return nil, fmt.Errorf("no service response for entity %s", entityID)
	}

	var haResp haItemsResponse
	if err := json.Unmarshal(raw, &haResp); err != nil {
		return nil, fmt.Errorf("parse items response for %s: %w", entityID, err)
	}

	items := make([]model.Item, 0, len(haResp.Items))
	for _, h := range haResp.Items {
		items = append(items, haItemToModelItem(h))
	}
	return items, nil
}
