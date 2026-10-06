package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3Config configures an S3-compatible Object Storage such as AWS S3 or MinIO.
type S3Config struct {
	// Endpoint is the URL the Server uses to reach the store.
	Endpoint string
	// PublicEndpoint is the URL Clients use in signed links. It defaults to Endpoint.
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	// PathStyle addresses the bucket as /bucket/key instead of bucket.host/key.
	PathStyle bool
}

type S3Storage struct {
	config S3Config
	client *http.Client
	now    func() time.Time
}

func NewS3Storage(config S3Config) (*S3Storage, error) {
	if config.PublicEndpoint == "" {
		config.PublicEndpoint = config.Endpoint
	}
	for name, value := range map[string]string{
		"endpoint": config.Endpoint, "region": config.Region, "bucket": config.Bucket,
		"access key": config.AccessKey, "secret key": config.SecretKey,
	} {
		if value == "" {
			return nil, fmt.Errorf("s3 %s is required", name)
		}
	}
	for _, endpoint := range []string{config.Endpoint, config.PublicEndpoint} {
		parsed, err := url.Parse(endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("s3 endpoint %q must be an http(s) URL", endpoint)
		}
	}
	return &S3Storage{config: config, client: &http.Client{Timeout: 15 * time.Second}, now: time.Now}, nil
}

func (s *S3Storage) PresignUpload(key string, request UploadRequest, ttl time.Duration) (Upload, error) {
	checksum, err := base64Checksum(request.ChecksumSHA256)
	if err != nil {
		return Upload{}, err
	}
	headers := map[string]string{"Content-Type": request.ContentType, "x-amz-checksum-sha256": checksum}
	signed, expiresAt := s.presign(http.MethodPut, s.config.PublicEndpoint, key, headers, nil, ttl)
	return Upload{URL: signed, Headers: headers, ExpiresAt: expiresAt}, nil
}

func (s *S3Storage) PresignDownload(key, filename, contentType string, ttl time.Duration) (Download, error) {
	query := url.Values{"response-content-disposition": {contentDisposition(filename, contentType)}}
	signed, expiresAt := s.presign(http.MethodGet, s.config.PublicEndpoint, key, nil, query, ttl)
	return Download{URL: signed, ExpiresAt: expiresAt}, nil
}

func (s *S3Storage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	headers := map[string]string{"x-amz-checksum-mode": "ENABLED"}
	signed, _ := s.presign(http.MethodHead, s.config.Endpoint, key, headers, nil, time.Minute)
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, signed, nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ObjectInfo{}, ErrObjectNotFound
	default:
		return ObjectInfo{}, fmt.Errorf("stat object: unexpected status %d", response.StatusCode)
	}
	size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return ObjectInfo{}, errors.New("stat object: missing Content-Length")
	}
	info := ObjectInfo{Size: size, ContentType: response.Header.Get("Content-Type")}
	if raw, err := base64.StdEncoding.DecodeString(response.Header.Get("x-amz-checksum-sha256")); err == nil && len(raw) == sha256.Size {
		info.ChecksumSHA256 = hex.EncodeToString(raw)
	}
	return info, nil
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	signed, _ := s.presign(http.MethodDelete, s.config.Endpoint, key, nil, nil, time.Minute)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, signed, nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	defer response.Body.Close()
	// S3 answers 204 whether or not the key existed.
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete object: unexpected status %d", response.StatusCode)
	}
	return nil
}

// presign builds an AWS Signature Version 4 query-string URL. Headers listed in
// signedHeaders must be sent unchanged with the request.
func (s *S3Storage) presign(method, endpoint, key string, signedHeaders map[string]string, extra url.Values, ttl time.Duration) (string, time.Time) {
	now := s.now().UTC()
	base, _ := url.Parse(endpoint)
	host := base.Host
	path := strings.TrimRight(base.Path, "/")
	if s.config.PathStyle {
		path += "/" + s.config.Bucket
	} else {
		host = s.config.Bucket + "." + host
	}
	path += "/" + key
	return signURL(signInput{
		Method: method, Scheme: base.Scheme, Host: host, Path: path, Region: s.config.Region,
		AccessKey: s.config.AccessKey, SecretKey: s.config.SecretKey, Now: now, TTL: ttl,
		Headers: signedHeaders, Query: extra,
	}), now.Add(ttl)
}

type signInput struct {
	Method, Scheme, Host, Path, Region, AccessKey, SecretKey string
	Now                                                      time.Time
	TTL                                                      time.Duration
	Headers                                                  map[string]string
	Query                                                    url.Values
}

func signURL(input signInput) string {
	date := input.Now.Format("20060102")
	timestamp := input.Now.Format("20060102T150405Z")
	scope := date + "/" + input.Region + "/s3/aws4_request"

	headers := map[string]string{"host": input.Host}
	for name, value := range input.Headers {
		headers[strings.ToLower(name)] = strings.TrimSpace(value)
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + headers[name] + "\n")
	}
	signedHeaderList := strings.Join(names, ";")

	query := url.Values{}
	for name, values := range input.Query {
		query[name] = values
	}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", input.AccessKey+"/"+scope)
	query.Set("X-Amz-Date", timestamp)
	query.Set("X-Amz-Expires", strconv.FormatInt(int64(input.TTL/time.Second), 10))
	query.Set("X-Amz-SignedHeaders", signedHeaderList)
	canonicalQuery := canonicalQueryString(query)

	canonicalPath := encodePath(input.Path)
	canonicalRequest := strings.Join([]string{
		input.Method, canonicalPath, canonicalQuery, canonicalHeaders.String(), signedHeaderList, "UNSIGNED-PAYLOAD",
	}, "\n")
	digest := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", timestamp, scope, hex.EncodeToString(digest[:])}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+input.SecretKey), date)
	for _, part := range []string{input.Region, "s3", "aws4_request"} {
		signingKey = hmacSHA256(signingKey, part)
	}
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	return input.Scheme + "://" + input.Host + canonicalPath + "?" + canonicalQuery + "&X-Amz-Signature=" + signature
}

func canonicalQueryString(query url.Values) string {
	pairs := make([]string, 0, len(query))
	for name, values := range query {
		for _, value := range values {
			pairs = append(pairs, encodeComponent(name)+"="+encodeComponent(value))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// encodeComponent percent-encodes everything except the RFC 3986 unreserved characters.
func encodeComponent(value string) string {
	var builder strings.Builder
	for _, b := range []byte(value) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.' || b == '~' {
			builder.WriteByte(b)
		} else {
			fmt.Fprintf(&builder, "%%%02X", b)
		}
	}
	return builder.String()
}

func encodePath(path string) string {
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		segments[index] = encodeComponent(segment)
	}
	return strings.Join(segments, "/")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func base64Checksum(hexValue string) (string, error) {
	raw, err := hex.DecodeString(hexValue)
	if err != nil || len(raw) != sha256.Size {
		return "", errors.New("checksum must be a SHA-256 hex digest")
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// contentDisposition forces a download unless the type is a raster image, so an
// uploaded HTML or SVG file can never run in the browser from the storage origin.
func contentDisposition(filename, contentType string) string {
	disposition := "attachment"
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		disposition = "inline"
	}
	return mime.FormatMediaType(disposition, map[string]string{"filename": filename})
}
