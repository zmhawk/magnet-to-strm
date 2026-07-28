package webdav

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"magnet-to-strm/internal/materialize"
)

const (
	davRoot     = "/dav"
	objectsRoot = "/dav/objects"
)

type Repository interface {
	AssetBySHA1(context.Context, string) (materialize.Asset, error)
	AssetsBySHA1Prefix(context.Context, string) ([]materialize.Asset, error)
	SHA1Prefixes(context.Context, string, int) ([]string, error)
}

type Resolver interface {
	Resolve(context.Context, string) (materialize.Resolution, error)
}

type Downloader interface {
	DownloadURL(context.Context, string, string) (string, error)
}

type Handler struct {
	Repository Repository
	Resolver   Resolver
	Downloader Downloader
	HTTPClient *http.Client
	Logf       func(string, ...any)
	urlCache   downloadURLCache
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("DAV", "1")
	writer.Header().Set("MS-Author-Via", "DAV")
	writer.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD")

	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusOK)
		return
	}

	resource, err := parseResource(request.URL.Path)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	switch request.Method {
	case "PROPFIND":
		h.propfind(writer, request, resource)
	case http.MethodGet, http.MethodHead:
		if resource.kind != resourceObject {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.proxyObject(writer, request, resource.sha1)
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) propfind(
	writer http.ResponseWriter,
	request *http.Request,
	resource resource,
) {
	depth := strings.TrimSpace(request.Header.Get("Depth"))
	if depth == "" {
		depth = "infinity"
	}
	if depth != "0" && depth != "1" {
		http.Error(writer, "Depth must be 0 or 1", http.StatusForbidden)
		return
	}

	entries, err := h.entries(request.Context(), resource, depth == "1")
	if errors.Is(err, materialize.ErrAssetNotFound) || errors.Is(err, errCollectionNotFound) {
		http.NotFound(writer, request)
		return
	}
	if err != nil {
		h.logf("读取 DAV 属性失败: %v", err)
		http.Error(writer, "failed to read properties", http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	writer.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(writer, xml.Header)
	if err := xml.NewEncoder(writer).Encode(multistatusFor(entries)); err != nil {
		h.logf("写入 DAV 属性失败: %v", err)
	}
}

func (h *Handler) entries(
	ctx context.Context,
	resource resource,
	includeChildren bool,
) ([]entry, error) {
	switch resource.kind {
	case resourceDAVRoot:
		result := []entry{collectionEntry(davRoot+"/", "dav")}
		if includeChildren {
			result = append(result, collectionEntry(objectsRoot+"/", "objects"))
		}
		return result, nil
	case resourceObjectsRoot:
		result := []entry{collectionEntry(objectsRoot+"/", "objects")}
		if !includeChildren {
			return result, nil
		}
		prefixes, err := h.Repository.SHA1Prefixes(ctx, "", 2)
		if err != nil {
			return nil, err
		}
		for _, prefix := range prefixes {
			result = append(result, collectionEntry(objectsRoot+"/"+prefix+"/", prefix))
		}
		return result, nil
	case resourceFirstBucket:
		prefixes, err := h.Repository.SHA1Prefixes(ctx, resource.prefix, 4)
		if err != nil {
			return nil, err
		}
		if len(prefixes) == 0 {
			return nil, errCollectionNotFound
		}
		href := objectsRoot + "/" + resource.prefix + "/"
		result := []entry{collectionEntry(href, resource.prefix)}
		if includeChildren {
			for _, prefix := range prefixes {
				result = append(result, collectionEntry(
					objectsRoot+"/"+prefix[:2]+"/"+prefix[2:]+"/",
					prefix[2:],
				))
			}
		}
		return result, nil
	case resourceSecondBucket:
		assets, err := h.Repository.AssetsBySHA1Prefix(ctx, resource.prefix)
		if err != nil {
			return nil, err
		}
		if len(assets) == 0 {
			return nil, errCollectionNotFound
		}
		href := objectsRoot + "/" + resource.prefix[:2] + "/" + resource.prefix[2:] + "/"
		result := []entry{collectionEntry(href, resource.prefix[2:])}
		if includeChildren {
			for index := range assets {
				result = append(result, objectEntry(assets[index], objectHref(assets[index])))
			}
		}
		return result, nil
	case resourceObject:
		asset, err := h.Repository.AssetBySHA1(ctx, resource.sha1)
		if err != nil {
			return nil, err
		}
		return []entry{objectEntry(asset, resource.href)}, nil
	default:
		return nil, errCollectionNotFound
	}
}

func (h *Handler) proxyObject(
	writer http.ResponseWriter,
	request *http.Request,
	sha1Value string,
) {
	resolution, err := h.Resolver.Resolve(request.Context(), sha1Value)
	if errors.Is(err, materialize.ErrAssetNotFound) {
		http.NotFound(writer, request)
		return
	}
	if err != nil {
		h.logf("解析 DAV 对象 %s 失败: %v", sha1Value, err)
		http.Error(writer, "failed to resolve object", http.StatusBadGateway)
		return
	}
	asset := resolution.Asset
	if strings.TrimSpace(resolution.Location.PickCode) == "" {
		h.logf("DAV 对象 %s 没有 115 pick_code", sha1Value)
		http.Error(writer, "object has no download code", http.StatusBadGateway)
		return
	}
	stableETag := `"` + asset.SHA1 + `"`
	if value := request.Header.Get("If-Match"); value != "" &&
		!etagMatches(value, stableETag) {
		writer.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	if value := request.Header.Get("If-None-Match"); value != "" &&
		etagMatches(value, stableETag) {
		writer.Header().Set("ETag", stableETag)
		writer.WriteHeader(http.StatusNotModified)
		return
	}

	response, err := h.download(request, resolution.Location.PickCode, stableETag)
	if err != nil {
		h.logf("从 115 下载 DAV 对象 %s 失败: %v", sha1Value, err)
		http.Error(writer, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(writer.Header(), response.Header)
	writer.Header().Set("ETag", stableETag)
	if !asset.CreatedAt.IsZero() {
		writer.Header().Set("Last-Modified", asset.CreatedAt.UTC().Format(http.TimeFormat))
	}
	writer.WriteHeader(response.StatusCode)
	if request.Method != http.MethodHead {
		_, _ = io.Copy(writer, response.Body)
	}
}

func (h *Handler) download(
	request *http.Request,
	pickCode string,
	stableETag string,
) (*http.Response, error) {
	userAgent := request.UserAgent()
	if userAgent == "" {
		userAgent = "magnet-to-strm"
	}
	cacheKey := pickCode + "\x00" + userAgent
	for attempt := 0; attempt < 2; attempt++ {
		cacheEntry, err := h.cachedDownloadURL(
			request.Context(), cacheKey, pickCode, userAgent,
		)
		if err != nil {
			return nil, fmt.Errorf("获取 115 下载地址: %w", err)
		}
		targetValue := cacheEntry.value
		target, err := url.Parse(targetValue)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") ||
			target.Host == "" {
			h.invalidateDownloadURL(cacheKey, targetValue)
			return nil, errors.New("115 返回了无效的下载地址")
		}

		upstream := request.Clone(request.Context())
		upstream.URL = target
		upstream.RequestURI = ""
		upstream.Host = target.Host
		upstream.Header = request.Header.Clone()
		removeHopHeaders(upstream.Header)
		upstream.Header.Del("Authorization")
		upstream.Header.Del("Cookie")
		upstream.Header.Del("Proxy-Authorization")
		upstream.Header.Del("If-Match")
		upstream.Header.Del("If-None-Match")
		upstream.Header.Del("If-Modified-Since")
		upstream.Header.Del("If-Unmodified-Since")
		upstream.Header.Set("User-Agent", userAgent)
		if value := upstream.Header.Get("If-Range"); value != "" {
			if !etagMatches(value, stableETag) {
				upstream.Header.Del("Range")
			}
			upstream.Header.Del("If-Range")
		}

		client := h.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		response, err := client.Do(upstream)
		if err != nil {
			return nil, err
		}
		if expiredDownloadStatus(response.StatusCode) {
			age := time.Since(cacheEntry.cachedAt)
			if age < 0 {
				age = 0
			}
			h.logf(
				"115 下载地址失效：pick_code=%s status=%d cached_at=%s age=%s user_agent=%q",
				pickCode, response.StatusCode,
				cacheEntry.cachedAt.UTC().Format(time.RFC3339),
				age.Round(time.Second), userAgent,
			)
			h.invalidateDownloadURL(cacheKey, targetValue)
			if attempt == 0 {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
				response.Body.Close()
				continue
			}
		}
		return response, nil
	}
	return nil, errors.New("115 下载地址刷新后仍不可用")
}

type downloadURLCache struct {
	mu      sync.Mutex
	entries map[string]downloadURLCacheEntry
	flights map[string]*downloadURLFlight
}

type downloadURLCacheEntry struct {
	value    string
	cachedAt time.Time
}

type downloadURLFlight struct {
	done  chan struct{}
	entry downloadURLCacheEntry
	err   error
}

func (h *Handler) cachedDownloadURL(
	ctx context.Context,
	key string,
	pickCode string,
	userAgent string,
) (downloadURLCacheEntry, error) {
	cache := &h.urlCache
	cache.mu.Lock()
	if entry := cache.entries[key]; entry.value != "" {
		cache.mu.Unlock()
		return entry, nil
	}
	if flight := cache.flights[key]; flight != nil {
		cache.mu.Unlock()
		select {
		case <-ctx.Done():
			return downloadURLCacheEntry{}, ctx.Err()
		case <-flight.done:
			return flight.entry, flight.err
		}
	}
	if cache.flights == nil {
		cache.flights = make(map[string]*downloadURLFlight)
	}
	flight := &downloadURLFlight{done: make(chan struct{})}
	cache.flights[key] = flight
	cache.mu.Unlock()

	value, err := h.Downloader.DownloadURL(ctx, pickCode, userAgent)
	entry := downloadURLCacheEntry{value: value, cachedAt: time.Now()}

	cache.mu.Lock()
	if err == nil {
		if cache.entries == nil {
			cache.entries = make(map[string]downloadURLCacheEntry)
		}
		cache.entries[key] = entry
	}
	flight.entry = entry
	flight.err = err
	close(flight.done)
	delete(cache.flights, key)
	cache.mu.Unlock()
	return entry, err
}

func (h *Handler) invalidateDownloadURL(key string, staleValue string) {
	cache := &h.urlCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries[key].value == staleValue {
		delete(cache.entries, key)
	}
}

func expiredDownloadStatus(status int) bool {
	return status == http.StatusUnauthorized ||
		status == http.StatusForbidden ||
		status == http.StatusGone
}

func (h *Handler) logf(format string, values ...any) {
	if h.Logf != nil {
		h.Logf(format, values...)
	}
}

type resourceKind int

const (
	resourceDAVRoot resourceKind = iota
	resourceObjectsRoot
	resourceFirstBucket
	resourceSecondBucket
	resourceObject
)

type resource struct {
	kind   resourceKind
	prefix string
	sha1   string
	href   string
}

func parseResource(value string) (resource, error) {
	clean := strings.TrimSuffix(value, "/")
	switch clean {
	case davRoot:
		return resource{kind: resourceDAVRoot}, nil
	case objectsRoot:
		return resource{kind: resourceObjectsRoot}, nil
	}
	if !strings.HasPrefix(clean, objectsRoot+"/") {
		return resource{}, errors.New("outside DAV root")
	}
	segments := strings.Split(strings.TrimPrefix(clean, objectsRoot+"/"), "/")
	switch len(segments) {
	case 1:
		if !validHex(segments[0], 2) {
			return resource{}, errors.New("invalid first bucket")
		}
		return resource{kind: resourceFirstBucket, prefix: segments[0]}, nil
	case 2:
		if !validHex(segments[0], 2) || !validHex(segments[1], 2) {
			return resource{}, errors.New("invalid second bucket")
		}
		return resource{
			kind: resourceSecondBucket, prefix: segments[0] + segments[1],
		}, nil
	case 3:
		if !validHex(segments[0], 2) || !validHex(segments[1], 2) {
			return resource{}, errors.New("invalid object buckets")
		}
		dot := strings.IndexByte(segments[2], '.')
		if dot < 0 || dot == len(segments[2])-1 {
			return resource{}, errors.New("object extension is required")
		}
		sha1Value := segments[2][:dot]
		if !validHex(sha1Value, 40) ||
			sha1Value[:2] != segments[0] || sha1Value[2:4] != segments[1] {
			return resource{}, errors.New("object path does not match SHA1")
		}
		return resource{
			kind: resourceObject, sha1: sha1Value, href: clean,
		}, nil
	default:
		return resource{}, errors.New("invalid DAV path")
	}
}

func validHex(value string, length int) bool {
	if len(value) != length || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func objectHref(asset materialize.Asset) string {
	extension := strings.ToLower(path.Ext(asset.PreferredName))
	if extension == "" {
		extension = ".bin"
	}
	return fmt.Sprintf(
		"%s/%s/%s/%s%s",
		objectsRoot, asset.SHA1[:2], asset.SHA1[2:4], asset.SHA1, extension,
	)
}

var errCollectionNotFound = errors.New("DAV collection not found")

type entry struct {
	href       string
	name       string
	collection bool
	asset      *materialize.Asset
}

func collectionEntry(href, name string) entry {
	return entry{href: href, name: name, collection: true}
}

func objectEntry(asset materialize.Asset, href string) entry {
	return entry{href: href, name: path.Base(href), asset: &asset}
}

type multistatus struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []davResponse `xml:"DAV: response"`
}

type davResponse struct {
	Href     string      `xml:"DAV: href"`
	Propstat davPropstat `xml:"DAV: propstat"`
}

type davPropstat struct {
	Prop   davProps `xml:"DAV: prop"`
	Status string   `xml:"DAV: status"`
}

type davProps struct {
	DisplayName   string          `xml:"DAV: displayname"`
	ResourceType  davResourceType `xml:"DAV: resourcetype"`
	ContentLength *int64          `xml:"DAV: getcontentlength,omitempty"`
	ContentType   string          `xml:"DAV: getcontenttype,omitempty"`
	ETag          string          `xml:"DAV: getetag,omitempty"`
	LastModified  string          `xml:"DAV: getlastmodified"`
	CreationDate  string          `xml:"DAV: creationdate"`
}

type davResourceType struct {
	Collection *struct{} `xml:"DAV: collection,omitempty"`
}

func multistatusFor(entries []entry) multistatus {
	result := multistatus{Responses: make([]davResponse, 0, len(entries))}
	for _, item := range entries {
		modified := time.Unix(0, 0).UTC()
		props := davProps{
			DisplayName:  item.name,
			LastModified: modified.Format(http.TimeFormat),
			CreationDate: modified.Format(time.RFC3339),
		}
		if item.collection {
			props.ResourceType.Collection = &struct{}{}
		} else {
			asset := item.asset
			length := asset.SizeBytes
			props.ContentLength = &length
			props.ContentType = mime.TypeByExtension(path.Ext(item.href))
			if props.ContentType == "" {
				props.ContentType = "application/octet-stream"
			}
			props.ETag = `"` + asset.SHA1 + `"`
			if !asset.CreatedAt.IsZero() {
				modified = asset.CreatedAt.UTC()
				props.LastModified = modified.Format(http.TimeFormat)
				props.CreationDate = modified.Format(time.RFC3339)
			}
		}
		result.Responses = append(result.Responses, davResponse{
			Href: item.href,
			Propstat: davPropstat{
				Prop: props, Status: "HTTP/1.1 200 OK",
			},
		})
	}
	return result
}

func removeHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			header.Del(strings.TrimSpace(token))
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		destination[name] = append([]string(nil), values...)
	}
	removeHopHeaders(destination)
	destination.Del("Set-Cookie")
}

func etagMatches(value, stableETag string) bool {
	for item := range strings.SplitSeq(value, ",") {
		item = strings.TrimSpace(item)
		if item == "*" || item == stableETag || strings.TrimPrefix(item, "W/") == stableETag {
			return true
		}
	}
	return false
}
