// Package backupstore implements the storage backends used by the console's
// RDB backup channel. The interface is deliberately file-oriented: the
// gateway receives a bounded stream into a private temporary file, verifies
// its hash, and only then hands the file to the selected backend. This keeps
// large backups out of process memory for both local/NFS and S3-compatible
// storage.
package backupstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Store is the minimal backend contract needed by the gateway and retention
// job. Ref values are persisted in Backup.Path and are opaque to callers.
type Store interface {
	Kind() string
	PutFile(ctx context.Context, id, path string, size int64, sha256 string) (ref string, err error)
	Open(ctx context.Context, ref string) (io.ReadCloser, error)
	Delete(ctx context.Context, ref string) error
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var prefixRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,160}$`)

func validID(id string) bool { return idRe.MatchString(id) }

// NewFromEnv selects the configured remote backend. With no S3 endpoint the
// console uses a normal filesystem directory; placing that directory on an
// NFS mount provides the NFS deployment described by the commercial plan.
func NewFromEnv(dataDir string) (Store, error) {
	root := strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_DIR"))
	if root == "" {
		root = filepath.Join(dataDir, "backups")
	}
	endpoint := strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_S3_ENDPOINT"))
	if endpoint == "" {
		return NewLocal(root)
	}
	pathStyle := true
	if raw := strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_S3_PATH_STYLE")); raw != "" {
		var err error
		pathStyle, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("CACHEOS_BACKUP_S3_PATH_STYLE: %w", err)
		}
	}
	allowHTTP := false
	if raw := strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_S3_ALLOW_HTTP")); raw != "" {
		var err error
		allowHTTP, err = strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("CACHEOS_BACKUP_S3_ALLOW_HTTP: %w", err)
		}
	}
	return NewS3(S3Config{
		Endpoint:   endpoint,
		Bucket:     strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_S3_BUCKET")),
		Region:     strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_S3_REGION")),
		AccessKey:  os.Getenv("CACHEOS_BACKUP_S3_ACCESS_KEY"),
		SecretKey:  os.Getenv("CACHEOS_BACKUP_S3_SECRET_KEY"),
		Prefix:     strings.Trim(strings.TrimSpace(os.Getenv("CACHEOS_BACKUP_S3_PREFIX")), "/"),
		PathStyle:  pathStyle,
		AllowHTTP:  allowHTTP,
		StageDir:   root,
		HTTPClient: http.DefaultClient,
	})
}

// Local stores files below Root. Root can be a local filesystem or an NFS
// mount; all published files are private and are made visible atomically.
type Local struct{ Root string }

func NewLocal(root string) (*Local, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("backup root is empty")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Local{Root: root}, nil
}

func (s *Local) Kind() string { return "console" }

func (s *Local) PutFile(ctx context.Context, id, path string, size int64, sum string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validID(id) {
		return "", errors.New("invalid backup id")
	}
	if _, _, err := validateFile(path, size, sum); err != nil {
		return "", err
	}
	dest := filepath.Join(s.Root, id+".rdb")
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(path, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (s *Local) safePath(ref string) (string, error) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(ref)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("backup path is outside the configured storage root")
	}
	return path, nil
}

func (s *Local) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.safePath(ref)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Local) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.safePath(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// S3Config describes an S3 or S3-compatible endpoint. Path-style addressing
// is the default because it works with MinIO and most private-object-storage
// gateways; set PathStyle=false for virtual-hosted AWS endpoints.
type S3Config struct {
	Endpoint   string
	Bucket     string
	Region     string
	AccessKey  string
	SecretKey  string
	Prefix     string
	PathStyle  bool
	AllowHTTP  bool
	StageDir   string
	HTTPClient *http.Client
}

type S3 struct {
	cfg      S3Config
	endpoint *url.URL
	client   *http.Client
}

func NewS3(cfg S3Config) (*S3, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || u.Scheme == "" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("S3 endpoint must be an absolute URL")
	}
	if u.Scheme != "https" && !(cfg.AllowHTTP && u.Scheme == "http") {
		return nil, errors.New("S3 endpoint must use HTTPS (set AllowHTTP only for an isolated test endpoint)")
	}
	if strings.TrimSpace(cfg.Bucket) == "" || strings.ContainsAny(cfg.Bucket, "/\\") {
		return nil, errors.New("S3 bucket is required")
	}
	if strings.TrimSpace(cfg.Region) == "" {
		cfg.Region = "us-east-1"
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, errors.New("S3 access and secret keys are required")
	}
	if !prefixRe.MatchString(cfg.Prefix) {
		return nil, errors.New("S3 prefix contains unsupported characters")
	}
	if cfg.StageDir != "" {
		if err := os.MkdirAll(cfg.StageDir, 0o700); err != nil {
			return nil, err
		}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &S3{cfg: cfg, endpoint: u, client: cfg.HTTPClient}, nil
}

func (s *S3) Kind() string { return "s3" }

func (s *S3) key(id string) string {
	if s.cfg.Prefix == "" {
		return id + ".rdb"
	}
	return s.cfg.Prefix + "/" + id + ".rdb"
}

func (s *S3) ref(key string) string { return "s3://" + s.cfg.Bucket + "/" + key }

func (s *S3) parseRef(ref string) (string, error) {
	prefix := "s3://" + s.cfg.Bucket + "/"
	if !strings.HasPrefix(ref, prefix) {
		return "", errors.New("backup reference belongs to another S3 bucket")
	}
	key := strings.TrimPrefix(ref, prefix)
	if key == "" || !prefixRe.MatchString(key) {
		return "", errors.New("invalid S3 backup reference")
	}
	return key, nil
}

func (s *S3) objectURL(key string) *url.URL {
	u := *s.endpoint
	base := strings.TrimRight(u.Path, "/")
	if s.cfg.PathStyle {
		u.Path = base + "/" + s.cfg.Bucket + "/" + key
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = base + "/" + key
	}
	u.RawQuery = ""
	return &u
}

func (s *S3) PutFile(ctx context.Context, id, path string, size int64, sum string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validID(id) || size < 0 || len(sum) != sha256.Size*2 {
		return "", errors.New("invalid S3 backup metadata")
	}
	actualSize, actualSum, err := validateFile(path, size, sum)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	resp, err := s.do(ctx, http.MethodPut, s.key(id), f, actualSize, actualSum)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return s.ref(s.key(id)), nil
}

func (s *S3) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	key, err := s.parseRef(ref)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, key, nil, 0, emptySHA256)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (s *S3) Delete(ctx context.Context, ref string) error {
	key, err := s.parseRef(ref)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, key, nil, 0, emptySHA256)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func validateFile(path string, expectedSize int64, expectedSum string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return 0, "", err
	} else if expectedSize >= 0 && fi.Size() != expectedSize {
		return 0, "", fmt.Errorf("backup size mismatch: got %d, want %d", fi.Size(), expectedSize)
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if expectedSum != "" && !strings.EqualFold(expectedSum, actual) {
		return 0, "", errors.New("backup sha256 mismatch")
	}
	return n, actual, nil
}

func (s *S3) do(ctx context.Context, method, key string, body io.Reader, size int64, payloadHash string) (*http.Response, error) {
	u := s.objectURL(key)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	now := time.Now().UTC()
	signV4(req, s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, now, payloadHash)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return nil, fmt.Errorf("S3 %s %s: HTTP %d: %s", method, key, resp.StatusCode, strings.TrimSpace(string(detail)))
}

func hmacBytes(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func signV4(req *http.Request, accessKey, secretKey, region string, now time.Time, payloadHash string) {
	amzDate := now.UTC().Format("20060102T150405Z")
	date := now.UTC().Format("20060102")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonicalHeaders := "host:" + strings.TrimSpace(req.URL.Host) + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalQuery := req.URL.Query().Encode()
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := strings.Join([]string{req.Method, canonicalURI, canonicalQuery, canonicalHeaders, strings.Join(signedHeaders, ";"), payloadHash}, "\n")
	scope := date + "/" + region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	kDate := hmacBytes([]byte("AWS4"+secretKey), date)
	kRegion := hmacBytes(kDate, region)
	kService := hmacBytes(kRegion, "s3")
	kSigning := hmacBytes(kService, "aws4_request")
	signature := hex.EncodeToString(hmacBytes(kSigning, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+strings.Join(signedHeaders, ";")+", Signature="+signature)
}
