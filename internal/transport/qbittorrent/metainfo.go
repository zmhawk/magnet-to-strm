package qbittorrent

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

func magnetFromTorrent(data []byte) (string, error) {
	decoder := bdecoder{data: data}
	value, start, end, err := decoder.value()
	if err != nil || decoder.offset != len(data) {
		return "", errors.New("无效的 torrent 元数据")
	}
	root, ok := value.(map[string]bvalue)
	if !ok {
		return "", errors.New("torrent 元数据不是字典")
	}
	info, ok := root["info"]
	if !ok {
		return "", errors.New("torrent 元数据缺少 info")
	}
	if info.start < start || info.end > end {
		return "", errors.New("torrent info 范围无效")
	}
	sum := sha1.Sum(data[info.start:info.end])
	query := url.Values{}
	query.Set("xt", "urn:btih:"+hex.EncodeToString(sum[:]))
	if dictionary, ok := info.value.(map[string]bvalue); ok {
		if name, ok := bytesValue(dictionary["name.utf-8"]); ok {
			query.Set("dn", name)
		} else if name, ok := bytesValue(dictionary["name"]); ok {
			query.Set("dn", name)
		}
	}
	if announce, ok := bytesValue(root["announce"]); ok {
		query.Add("tr", announce)
	}
	if tiers, ok := root["announce-list"].value.([]bvalue); ok {
		for _, tier := range tiers {
			if trackers, ok := tier.value.([]bvalue); ok {
				for _, tracker := range trackers {
					if value, ok := bytesValue(tracker); ok && value != query.Get("tr") {
						query.Add("tr", value)
					}
				}
			}
		}
	}
	return "magnet:?" + query.Encode(), nil
}

type bvalue struct {
	value      any
	start, end int
}

type bdecoder struct {
	data   []byte
	offset int
	depth  int
}

func (d *bdecoder) value() (any, int, int, error) {
	if d.offset >= len(d.data) || d.depth > 100 {
		return nil, 0, 0, errors.New("无效的 bencode")
	}
	start := d.offset
	d.depth++
	defer func() { d.depth-- }()
	switch d.data[d.offset] {
	case 'i':
		d.offset++
		end := d.indexByte('e')
		if end < 0 {
			return nil, 0, 0, errors.New("无效的 bencode 整数")
		}
		number, err := strconv.ParseInt(string(d.data[d.offset:end]), 10, 64)
		if err != nil {
			return nil, 0, 0, err
		}
		d.offset = end + 1
		return number, start, d.offset, nil
	case 'l':
		d.offset++
		var values []bvalue
		for d.offset < len(d.data) && d.data[d.offset] != 'e' {
			value, childStart, childEnd, err := d.value()
			if err != nil {
				return nil, 0, 0, err
			}
			values = append(values, bvalue{value: value, start: childStart, end: childEnd})
		}
		if d.offset >= len(d.data) {
			return nil, 0, 0, errors.New("未结束的 bencode 列表")
		}
		d.offset++
		return values, start, d.offset, nil
	case 'd':
		d.offset++
		values := make(map[string]bvalue)
		for d.offset < len(d.data) && d.data[d.offset] != 'e' {
			key, err := d.byteString()
			if err != nil {
				return nil, 0, 0, err
			}
			value, childStart, childEnd, err := d.value()
			if err != nil {
				return nil, 0, 0, err
			}
			values[string(key)] = bvalue{value: value, start: childStart, end: childEnd}
		}
		if d.offset >= len(d.data) {
			return nil, 0, 0, errors.New("未结束的 bencode 字典")
		}
		d.offset++
		return values, start, d.offset, nil
	default:
		value, err := d.byteString()
		return value, start, d.offset, err
	}
}

func (d *bdecoder) byteString() ([]byte, error) {
	colon := d.indexByte(':')
	if colon < 0 {
		return nil, errors.New("无效的 bencode 字符串")
	}
	length, err := strconv.Atoi(string(d.data[d.offset:colon]))
	if err != nil || length < 0 {
		return nil, errors.New("无效的 bencode 字符串长度")
	}
	d.offset = colon + 1
	if length > len(d.data)-d.offset {
		return nil, errors.New("bencode 字符串越界")
	}
	value := d.data[d.offset : d.offset+length]
	d.offset += length
	return value, nil
}

func (d *bdecoder) indexByte(target byte) int {
	for index := d.offset; index < len(d.data); index++ {
		if d.data[index] == target {
			return index
		}
	}
	return -1
}

func bytesValue(value bvalue) (string, bool) {
	bytes, ok := value.value.([]byte)
	if !ok {
		return "", false
	}
	if len(bytes) == 0 {
		return "", false
	}
	return string(bytes), true
}

func unsupportedTorrentMessage(err error) error {
	return fmt.Errorf("读取 torrent 文件: %w", err)
}
