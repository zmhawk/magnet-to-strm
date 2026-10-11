package p115

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"magnet-to-strm/internal/config"
	"resty.dev/v3"
)

type updateTokenRepository struct {
	tokenRepositoryStub
	value Credential
}

func (r *updateTokenRepository) LoadCredential(context.Context, string) (Credential, error) {
	return r.value, nil
}
func (r *updateTokenRepository) SaveCredential(_ context.Context, _ string, value Credential) error {
	r.value = value
	return nil
}

type localTokenTransport struct{ target *url.URL }

func (r localTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestLiveTokensUseLatestDatabaseRefreshTokenAndRetry(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	repository := &updateTokenRepository{value: Credential{AccessToken: "A2", RefreshToken: "R2", ExpiresAt: &expires}}
	refreshes, probes := 0, 0
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open/user/info" {
			probes++
			if r.Header.Get("Authorization") == "Bearer invalid" {
				fmt.Fprint(w, `{"state":false,"code":401,"message":"invalid token"}`)
			} else {
				fmt.Fprint(w, `{"state":true,"data":{}}`)
			}
			return
		}
		refreshes++
		if fail {
			fmt.Fprint(w, `{"code":1,"message":"temporary failure"}`)
			return
		}
		r.ParseForm()
		if r.Form.Get("refresh_token") != "R2" && r.Form.Get("refresh_token") != "R3" && r.Form.Get("refresh_token") != "R4" {
			t.Errorf("unexpected refresh source: %s", r.Form.Get("refresh_token"))
		}
		fmt.Fprint(w, `{"code":0,"data":{"access_token":"A4","refresh_token":"R4","expires_in":3600}}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := NewTokenEditor(repository, config.P115{}, nil)
	client.tokenSDK = func(value Credential) *sdk.Client {
		return sdk.New(sdk.WithRestyClient(resty.New().SetTransport(localTokenTransport{target})), sdk.WithAccessToken(value.AccessToken), sdk.WithRefreshToken(value.RefreshToken))
	}
	ctx := context.Background()
	auth := "A3"
	value, refreshed, err := client.UpdateTokens(ctx, nil, &auth)
	if err != nil || refreshed || refreshes != 0 || value.RefreshToken != "R2" || value.AccessToken != "A3" || !client.Available() {
		t.Fatalf("auth-only update: %+v %t %v", value, refreshed, err)
	}
	refresh := "R3"
	fail = true
	_, _, err = client.UpdateTokens(ctx, &refresh, nil)
	if err == nil {
		t.Fatal("refresh failure was ignored")
	}
	fail = false
	value, refreshed, err = client.UpdateTokens(ctx, &refresh, nil)
	if err != nil || !refreshed || value.RefreshToken != "R4" || value.AccessToken != "A4" || refreshes != 2 {
		t.Fatalf("same-value retry: %+v %t %v count=%d", value, refreshed, err, refreshes)
	}
	value, refreshed, err = client.UpdateTokens(ctx, nil, nil)
	if err != nil || refreshed || refreshes != 2 || probes != 2 {
		t.Fatalf("valid unchanged tokens must only be checked: %+v %t %v", value, refreshed, err)
	}
	auth = "invalid"
	value, refreshed, err = client.UpdateTokens(ctx, nil, &auth)
	if err != nil || !refreshed || value.AccessToken != "A4" || refreshes != 3 {
		t.Fatalf("invalid access must refresh: %+v %t %v", value, refreshed, err)
	}
}

func TestTokensMissingRepositoryRow(t *testing.T) {
	client := NewTokenEditor(&tokenRepositoryStub{}, config.P115{}, nil)
	value, err := client.Tokens(context.Background())
	if err != nil || value.RefreshToken != "" {
		t.Fatal(value, err)
	}
	_, _, err = client.UpdateTokens(context.Background(), nil, nil)
	if err == nil || errors.Is(err, ErrCredentialNotFound) {
		t.Fatal("missing refresh token must have a useful validation error")
	}
}
