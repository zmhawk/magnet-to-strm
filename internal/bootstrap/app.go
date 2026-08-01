package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"magnet-to-strm/internal/config"
	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/logging"
	"magnet-to-strm/internal/materialize"
	"magnet-to-strm/internal/provider/p115"
	"magnet-to-strm/internal/storage/sqlite"
	"magnet-to-strm/internal/strm"
	"magnet-to-strm/internal/transport/aria2"
	"magnet-to-strm/internal/transport/httpserver"
	"magnet-to-strm/internal/transport/qbittorrent"
	"magnet-to-strm/internal/transport/webdav"
)

type Core struct {
	Config config.Config
	DB     *sqlite.DB
	P115   *p115.Client
	Ingest *ingest.Service
	STRM   *strm.Service
	Logger *logging.Logger
	Stderr io.Writer
}

func OpenCore(
	ctx context.Context,
	cfg config.Config,
	secrets config.Secrets,
	stderr io.Writer,
) (*Core, error) {
	stderr = logging.NewTimestampWriter(stderr)
	return openCore(ctx, cfg, secrets, stderr, false)
}

func openCore(
	ctx context.Context,
	cfg config.Config,
	secrets config.Secrets,
	stderr io.Writer,
	optionalP115 bool,
) (*Core, error) {
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, err
	}
	logger, err := logging.New(stderr, cfg.Logging.Level)
	if err != nil {
		return nil, err
	}
	strmStore, err := strm.New(cfg.Library.STRMDir, cfg.HTTP.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	database, err := sqlite.Open(cfg.Database.Path)
	if err != nil {
		return nil, err
	}
	var client *p115.Client
	if optionalP115 {
		client, err = p115.NewOptional(
			ctx, database, cfg.P115, secrets.P115RefreshToken, logger.Writer(),
		)
	} else {
		client, err = p115.New(
			ctx, database, cfg.P115, secrets.P115RefreshToken, logger.Writer(),
		)
	}
	if err != nil {
		database.Close()
		return nil, err
	}
	logf := logger.Printf
	service := &ingest.Service{
		Provider: client, Repository: database, WorkDirID: cfg.P115.WorkDirID,
		STRMStore: strmStore, PollInterval: cfg.P115.OfflinePoll,
		PollMinInterval: cfg.P115.OfflinePollMin,
		PollMaxInterval: cfg.P115.OfflinePollMax,
		ScanInterval:    0, BackgroundContext: ctx,
		BackgroundTimeout: cfg.Ingest.JobTimeout, Logf: logf,
	}
	return &Core{
		Config: cfg, DB: database, P115: client, Ingest: service,
		STRM: strmStore, Logger: logger, Stderr: stderr,
	}, nil
}

func (c *Core) Close() error {
	return c.DB.Close()
}

type Server struct {
	Core         *Core
	HTTP         *http.Server
	Materializer *materialize.Service
	Aria2        *aria2.Manager
	WebDAV       *webdav.Handler
	P115Enabled  bool
}

func OpenServer(
	ctx context.Context,
	cfg config.Config,
	secrets config.Secrets,
	stderr io.Writer,
) (*Server, error) {
	stderr = logging.NewTimestampWriter(stderr)
	if err := cfg.ValidateServe(); err != nil {
		return nil, err
	}
	core, err := openCore(ctx, cfg, secrets, stderr, true)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			core.Close()
		}
	}()
	redirectBase, err := url.Parse(cfg.HTTP.RedirectBaseURL)
	if err != nil {
		return nil, err
	}
	logf := core.Logger.Printf
	resolutionCache := &materialize.ResolutionCache{}
	restorer := &offlineContentRestorer{
		ingest: core.Ingest,
	}
	materializer := &materialize.Service{
		Provider: core.P115, Downloader: core.P115, OfflineTasks: core.P115,
		Repository: core.DB, WorkDirID: cfg.P115.WorkDirID,
		Restorer: restorer, RedirectBaseURL: redirectBase,
		RedirectType: cfg.HTTP.RedirectType, CacheTTL: cfg.HTTP.MaterializeCacheTTL,
		CacheMaxSizeBytes: cfg.Library.CacheMaxSizeBytes,
		OperationContext:  ctx, OperationTimeout: cfg.Ingest.JobTimeout,
		ResolutionCache: resolutionCache, Logf: logf,
	}
	p115Enabled := core.P115.Available() && cfg.P115.WorkDirID != ""
	var ariaManager *aria2.Manager
	if p115Enabled {
		ariaManager, err = aria2.NewManager(
			ctx, core.Ingest, core.DB, cfg.Ingest.JobTimeout, logf,
			cfg.P115.OfflineQuotaMinRemaining,
		)
		if err != nil {
			return nil, err
		}
	} else {
		reason := p115.ErrUnavailable
		if core.P115.Available() {
			reason = errors.New("未配置 p115.work_dir_id，115 操作不可用")
		}
		ariaManager = aria2.NewDisabledManager(
			ctx, core.Ingest, core.DB, cfg.Ingest.JobTimeout, logf, reason,
		)
	}
	ariaHandler := &aria2.Handler{
		Manager: ariaManager, Secret: secrets.Aria2RPCSecret, Dir: core.STRM.RootDir,
	}
	qbitHandler := qbittorrent.NewHandler(
		ariaManager, core.STRM.RootDir, secrets.QBitUsername, secrets.QBitPassword,
	)
	davHandler := &webdav.Handler{
		Repository: core.DB, Resolver: materializer, Downloader: core.P115,
		HTTPClient: &http.Client{}, Logf: logf,
	}
	handler := httpserver.NewHandler(
		materializer, core.DB, core.DB, ariaManager,
		ariaHandler, davHandler, p115Enabled, logf,
		qbitHandler,
	)
	server := &http.Server{
		Addr: cfg.HTTP.Addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	cleanup = false
	return &Server{
		Core: core, HTTP: server, Materializer: materializer,
		Aria2: ariaManager, WebDAV: davHandler,
		P115Enabled: p115Enabled,
	}, nil
}

type offlineContentRestorer struct {
	ingest *ingest.Service
}

func (r *offlineContentRestorer) RestoreContent(
	ctx context.Context,
	magnetURI string,
	sha1Value string,
) (materialize.RemoteFile, error) {
	file, err := r.ingest.RestoreContent(ctx, magnetURI, sha1Value)
	if err != nil {
		return materialize.RemoteFile{}, err
	}
	if !strings.EqualFold(file.SHA1, sha1Value) {
		return materialize.RemoteFile{}, fmt.Errorf(
			"重建后的 115 文件 SHA1 不匹配：得到 %s，预期 %s",
			file.SHA1, sha1Value,
		)
	}
	return materialize.RemoteFile{
		ID:         file.RemoteID,
		ParentID:   file.ParentID,
		Name:       file.Name,
		SHA1:       file.SHA1,
		SizeBytes:  file.SizeBytes,
		PickCode:   file.PickCode,
		RemotePath: file.RemotePath,
	}, nil
}

func (s *Server) Close() error {
	return s.Core.Close()
}

func DisplayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}
