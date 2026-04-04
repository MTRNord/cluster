// matrix.go — low-level Matrix client helpers not wrapped by mautrix.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"maunium.net/go/mautrix"
)

// matrixGetJSON makes an authenticated GET to the homeserver and JSON-decodes
// the response. Used for endpoints not wrapped by the mautrix client.
func matrixGetJSON(ctx context.Context, client *mautrix.Client, path string, out interface{}) error {
	base := strings.TrimRight(client.HomeserverURL.String(), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// getAccountData fetches a single account-data event for the authenticated user.
func getAccountData(ctx context.Context, client *mautrix.Client, eventType string, out interface{}) error {
	path := "/_matrix/client/v3/user/" +
		url.PathEscape(client.UserID.String()) +
		"/account_data/" +
		url.PathEscape(eventType)
	return matrixGetJSON(ctx, client, path, out)
}
