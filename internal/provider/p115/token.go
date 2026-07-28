package p115

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
	"resty.dev/v3"

	"magnet-to-strm/internal/config"
)

const credentialProvider = "p115"

const (
	refreshLeaseDuration  = time.Minute
	refreshFailureBackoff = 10 * time.Minute
	optionalStartupProbe  = 3 * time.Second
)

var (
	ErrCredentialNotFound = errors.New("凭证不存在")
	ErrUnavailable        = errors.New("115 未配置，当前操作不可用")
)

type Credential struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
}

type CredentialRepository interface {
	LoadCredential(context.Context, string) (Credential, error)
	SeedCredential(context.Context, string, Credential) error
	SaveCredential(context.Context, string, Credential) error
	ReconcileRefreshToken(context.Context, string, string, string) (bool, error)
	AcquireLease(context.Context, string, string, time.Time) (bool, error)
	ReleaseLease(context.Context, string, string) error
}

type Client struct {
	sdk          *sdk.Client
	credentials  CredentialRepository
	refreshAhead time.Duration
	stderr       io.Writer
	owner        string
	refreshMu    sync.Mutex
	stateMu      sync.RWMutex
	credential   Credential
	limiter      apiLimiter
	refresh      func(context.Context) (*sdk.RefreshTokenResp, error)
	available    bool
}

func New(
	ctx context.Context,
	repository CredentialRepository,
	cfg config.P115,
	refreshToken string,
	stderr io.Writer,
) (*Client, error) {
	client, err := newClient(ctx, repository, cfg, refreshToken, stderr)
	if err != nil || !client.Available() {
		return client, err
	}
	if err := client.before(ctx); err != nil {
		return nil, err
	}
	client.after()
	return client, nil
}

// NewOptional builds a client for the long-running HTTP server. If the stored
// credential cannot be refreshed promptly, the client is disabled for this
// process so local database and WebUI features can still start.
func NewOptional(
	ctx context.Context,
	repository CredentialRepository,
	cfg config.P115,
	refreshToken string,
	stderr io.Writer,
) (*Client, error) {
	client, err := newClient(ctx, repository, cfg, refreshToken, stderr)
	if err != nil || !client.Available() {
		return client, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, optionalStartupProbe)
	defer cancel()
	if err := client.before(probeCtx); err != nil {
		client.available = false
		reason := err
		if errors.Is(err, context.DeadlineExceeded) {
			reason = errors.New("TOKEN 刷新探测超时")
		}
		if stderr != nil {
			fmt.Fprintf(
				stderr,
				"警告: 115 TOKEN 暂不可用，将以本地数据库模式启动: %v\n",
				reason,
			)
		}
	} else {
		client.after()
	}
	return client, nil
}

func newClient(
	ctx context.Context,
	repository CredentialRepository,
	cfg config.P115,
	refreshToken string,
	stderr io.Writer,
) (*Client, error) {
	credential, err := repository.LoadCredential(ctx, credentialProvider)
	if errors.Is(err, ErrCredentialNotFound) {
		if refreshToken == "" {
			if stderr != nil {
				fmt.Fprintln(stderr, "未配置 115 TOKEN，将以本地数据库模式启动")
			}
			return &Client{
				credentials:  repository,
				refreshAhead: cfg.TokenRefreshAhead,
				stderr:       stderr,
				owner:        uuid.NewString(),
				limiter: newAPILimiter(
					cfg.RequestRate, cfg.RequestBurst, cfg.RequestConcurrency,
				),
			}, nil
		}
		expired := time.Unix(0, 0).UTC()
		credential = Credential{
			RefreshToken: refreshToken,
			ExpiresAt:    &expired,
		}
		if err := repository.SeedCredential(
			ctx, credentialProvider, credential,
		); err != nil {
			return nil, fmt.Errorf("初始化 115 凭证: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取 115 凭证: %w", err)
	}
	if refreshToken != "" {
		fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(refreshToken)))
		replaced, err := repository.ReconcileRefreshToken(
			ctx, credentialProvider, refreshToken, fingerprint,
		)
		if err != nil {
			return nil, fmt.Errorf("同步环境变量中的 115 Refresh Token: %w", err)
		}
		if replaced {
			credential, err = repository.LoadCredential(ctx, credentialProvider)
			if err != nil {
				return nil, fmt.Errorf("重新读取 115 凭证: %w", err)
			}
			if stderr != nil {
				fmt.Fprintln(stderr, "检测到环境变量变化，已导入新的 115 Refresh Token")
			}
		}
	}
	client := &Client{
		credentials:  repository,
		refreshAhead: cfg.TokenRefreshAhead,
		stderr:       stderr,
		owner:        uuid.NewString(),
		credential:   credential,
		limiter: newAPILimiter(
			cfg.RequestRate, cfg.RequestBurst, cfg.RequestConcurrency,
		),
		available: true,
	}
	client.sdk = sdk.New(
		sdk.WithRestyClient(newRestyClient(stderr)),
		sdk.WithAccessToken(credential.AccessToken),
		sdk.WithRefreshToken(credential.RefreshToken),
		sdk.WithOnRefreshToken(func(accessToken, refreshToken string) {
			refreshed := Credential{AccessToken: accessToken, RefreshToken: refreshToken}
			persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := repository.SaveCredential(
				persistCtx, credentialProvider, refreshed,
			); err != nil && stderr != nil {
				fmt.Fprintf(stderr, "警告: 115 TOKEN 已刷新，但持久化失败: %v\n", err)
			}
			client.setCredential(refreshed)
		}),
	)
	client.refresh = client.sdk.RefreshToken
	return client, nil
}

func newRestyClient(stderr io.Writer) *resty.Client {
	client := resty.New().SetDisableWarn(true)
	if stderr == nil {
		return client
	}
	client.SetRequestMiddlewares(
		resty.PrepareRequestMiddleware,
		func(_ *resty.Client, request *resty.Request) error {
			if request.RawRequest != nil {
				fmt.Fprintf(
					stderr,
					"115 API: %s %s\n",
					request.RawRequest.Method,
					sanitizedRequestURL(request.RawRequest.URL),
				)
			}
			return nil
		},
	)
	return client
}

func sanitizedRequestURL(requestURL *url.URL) string {
	if requestURL == nil {
		return ""
	}
	sanitized := *requestURL
	query := sanitized.Query()
	for key := range query {
		switch strings.ToLower(key) {
		case "access_token", "refresh_token", "token", "authorization",
			"cookie", "password", "passwd":
			query.Set(key, "[REDACTED]")
		}
	}
	sanitized.RawQuery = query.Encode()
	return sanitized.String()
}

func (c *Client) Available() bool {
	return c != nil && c.available
}

func (c *Client) before(ctx context.Context) error {
	if !c.Available() {
		return ErrUnavailable
	}
	if err := c.limiter.wait(ctx); err != nil {
		return err
	}
	if err := c.limiter.acquire(ctx); err != nil {
		return err
	}
	if err := c.ensureFresh(ctx); err != nil {
		c.limiter.release()
		return err
	}
	return nil
}

func (c *Client) after() {
	c.limiter.release()
}

func (c *Client) ensureFresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	current := c.getCredential()
	if current.ExpiresAt == nil || time.Until(*current.ExpiresAt) > c.refreshAhead {
		return nil
	}
	const leaseName = "credential-refresh:p115"
	acquired, err := c.credentials.AcquireLease(
		ctx, leaseName, c.owner, time.Now().Add(refreshLeaseDuration),
	)
	if err != nil {
		return fmt.Errorf("获取 TOKEN 刷新租约: %w", err)
	}
	if !acquired {
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			latest, err := c.credentials.LoadCredential(ctx, credentialProvider)
			if err == nil && latest.AccessToken != current.AccessToken {
				c.sdk.SetAccessToken(latest.AccessToken)
				c.sdk.SetRefreshToken(latest.RefreshToken)
				c.setCredential(latest)
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return errors.New("等待其他进程刷新 115 TOKEN 超时")
			case <-ticker.C:
			}
		}
	}
	releaseLease := true
	defer func() {
		if releaseLease {
			_ = c.credentials.ReleaseLease(context.Background(), leaseName, c.owner)
		}
	}()

	response, err := c.refresh(ctx)
	if err != nil {
		cooldownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		held, leaseErr := c.credentials.AcquireLease(
			cooldownCtx, leaseName, c.owner, time.Now().Add(refreshFailureBackoff),
		)
		if leaseErr == nil && held {
			releaseLease = false
			if c.stderr != nil {
				fmt.Fprintf(
					c.stderr,
					"115 TOKEN 刷新失败，%s 内不会再次尝试\n",
					refreshFailureBackoff,
				)
			}
		} else if leaseErr != nil && c.stderr != nil {
			fmt.Fprintf(c.stderr, "警告: 保存 TOKEN 刷新冷却期失败: %v\n", leaseErr)
		}
		return fmt.Errorf("刷新 115 TOKEN: %w", err)
	}
	if response.AccessToken == "" || response.RefreshToken == "" || response.ExpiresIn <= 0 {
		return errors.New("115 TOKEN 刷新响应不完整")
	}
	expiry := time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	refreshed := Credential{
		AccessToken: response.AccessToken, RefreshToken: response.RefreshToken,
		ExpiresAt: &expiry,
	}
	if err := c.credentials.SaveCredential(
		ctx, credentialProvider, refreshed,
	); err != nil {
		return fmt.Errorf("保存 115 TOKEN: %w", err)
	}
	c.setCredential(refreshed)
	if c.stderr != nil {
		fmt.Fprintln(c.stderr, "115 Access Token 临近过期，已刷新并持久化")
	}
	return nil
}

func (c *Client) getCredential() Credential {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.credential
}

func (c *Client) setCredential(credential Credential) {
	c.stateMu.Lock()
	c.credential = credential
	c.stateMu.Unlock()
}

type apiLimiter struct {
	limiter    *rate.Limiter
	concurrent chan struct{}
}

func newAPILimiter(requestsPerSecond float64, burst, concurrency int) apiLimiter {
	if requestsPerSecond <= 0 {
		requestsPerSecond = 1
	}
	if burst <= 0 {
		burst = 2
	}
	if concurrency <= 0 {
		concurrency = 2
	}
	return apiLimiter{
		limiter:    rate.NewLimiter(rate.Limit(requestsPerSecond), burst),
		concurrent: make(chan struct{}, concurrency),
	}
}

func (l *apiLimiter) acquire(ctx context.Context) error {
	if l.concurrent == nil {
		return nil
	}
	select {
	case l.concurrent <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *apiLimiter) release() {
	if l.concurrent != nil {
		<-l.concurrent
	}
}

func (l *apiLimiter) wait(ctx context.Context) error {
	if l.limiter == nil {
		return nil
	}
	return l.limiter.Wait(ctx)
}
