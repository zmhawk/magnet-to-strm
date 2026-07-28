package ingest

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

func ParseInfoHash(magnetURI string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(magnetURI))
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") {
		return "", errors.New("输入不是有效的磁力链接")
	}
	for _, exactTopic := range parsed.Query()["xt"] {
		const prefix = "urn:btih:"
		if !strings.HasPrefix(strings.ToLower(exactTopic), prefix) {
			continue
		}
		value := strings.TrimSpace(exactTopic[len(prefix):])
		switch len(value) {
		case 40:
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != 20 {
				return "", errors.New("磁力链接中的 BTIH 无效")
			}
			return strings.ToLower(value), nil
		case 32:
			decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).
				DecodeString(strings.ToUpper(value))
			if err != nil || len(decoded) != 20 {
				return "", errors.New("磁力链接中的 BTIH 无效")
			}
			return hex.EncodeToString(decoded), nil
		default:
			return "", errors.New("磁力链接中的 BTIH 长度无效")
		}
	}
	return "", errors.New("磁力链接缺少 urn:btih")
}
