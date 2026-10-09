package offsite

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeAPI is a PutObjectAPI that records calls, for tests that need no HTTP.
type fakeAPI struct{ calls int }

func (f *fakeAPI) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.calls++
	return &s3.PutObjectOutput{}, nil
}

// fakeS3 is an S3 endpoint that stores PUT bodies and answers with the
// sha256 of what it received — or, when lie is set, with a different one.
type fakeS3 struct {
	mu       sync.Mutex
	requests []fakeReq
	lie      bool
	noSum    bool
}

type fakeReq struct {
	Method, Path, Query string
	SentChecksum        string
	Body                []byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, fakeReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("x-amz-checksum-sha256"), body})
	lie, noSum := f.lie, f.noSum
	f.mu.Unlock()
	if r.Method != http.MethodPut {
		http.Error(w, "this fake is put-only, like the principal", http.StatusForbidden)
		return
	}
	sum := sha256.Sum256(body)
	if lie {
		sum[0] ^= 0xff
	}
	m := md5.Sum(body)
	w.Header().Set("ETag", `"`+hex.EncodeToString(m[:])+`"`)
	if !noSum {
		w.Header().Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(sum[:]))
	}
	w.WriteHeader(http.StatusOK)
}

// fakeClient is the real SDK client pointed at the fake. The endpoint
// override lives HERE, in a test, and nowhere in production code.
func fakeClient(t *testing.T, f *fakeS3) *s3.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg := aws.Config{
		Region:      "us-west-2",
		Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "example-secret", ""),
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.UsePathStyle = true
	})
}

// plaintextZip is a backup-shaped zip whose bytes contain both markers the
// "S3 never sees plaintext" assertion looks for, stored uncompressed so the
// SQLite header is visible in it.
func plaintextZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "hz.db", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{7}, 4096)...))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if !bytes.Contains(b, []byte("SQLite format 3")) || !bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		t.Fatal("instrument check: the plaintext fixture does not carry the markers")
	}
	return b
}

var pushAt = time.Date(2026, 10, 9, 4, 5, 6, 0, time.UTC)

func push(t *testing.T, f *fakeS3, zipData []byte, recipients []Recipient) (*PushResult, error) {
	t.Helper()
	return Push(t.Context(), fakeClient(t, f), Dest{Bucket: "example-hz-backups", Prefix: "hz-office"},
		zipData, testMeta(), recipients, pushAt, t.TempDir())
}

func TestPushUploadsTheObjectThenTheHeaderWithPutObjectOnly(t *testing.T) {
	k := testKey(t)
	f := &fakeS3{}
	zipData := plaintextZip(t)
	res, err := push(t, f, zipData, []Recipient{{Name: "offline-2026-10", PublicKey: k.PublicKey()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d requests, want 2", len(f.requests))
	}
	base := "/example-hz-backups/hz-office/gw.example.com/20261009T040506Z"
	for i, want := range []string{base + ObjectSuffix, base + SidecarSuffix} {
		r := f.requests[i]
		if r.Method != http.MethodPut || r.Path != want {
			t.Fatalf("request %d: %s %s, want PUT %s", i, r.Method, r.Path, want)
		}
		if !strings.Contains(r.Query, "x-id=PutObject") {
			t.Fatalf("request %d is not PutObject: ?%s", i, r.Query)
		}
		sum := sha256.Sum256(r.Body)
		if r.SentChecksum != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Fatalf("request %d: sent x-amz-checksum-sha256 %q is not the body's", i, r.SentChecksum)
		}
	}

	// S3 never sees plaintext: neither marker is in anything uploaded.
	for i, r := range f.requests {
		if bytes.Contains(r.Body, []byte("SQLite format 3")) || bytes.Contains(r.Body, []byte("PK\x03\x04")) {
			t.Fatalf("request %d carries plaintext", i)
		}
	}

	// The object is the ciphertext the sidecar describes, and it decrypts to
	// exactly the zip.
	obj := f.requests[0].Body
	var side map[string]any
	if err := json.Unmarshal(f.requests[1].Body, &side); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range side {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := jsonKeys(t, reflect.TypeOf(Sidecar{})); !reflect.DeepEqual(keys, want) {
		t.Fatalf("uploaded sidecar keys %v, want %v", keys, want)
	}
	sum := sha256.Sum256(obj)
	if side["ciphertext_sha256"] != hex.EncodeToString(sum[:]) || res.Header.CiphertextBytes != int64(len(obj)) {
		t.Fatalf("sidecar does not describe the object: %v", side)
	}
	if side["object"] != "hz-office/gw.example.com/20261009T040506Z.hzbak" {
		t.Fatalf("sidecar object %v", side["object"])
	}
	var out bytes.Buffer
	if _, err := Decrypt(bytes.NewReader(obj), k, &out); err != nil || !bytes.Equal(out.Bytes(), zipData) {
		t.Fatalf("the uploaded object does not decrypt to the zip: %v", err)
	}
}

// S3 answering with a checksum that is not what was sent is a FAILURE, the
// header is not uploaded, and the error says so loudly.
func TestPushChecksumMismatchFails(t *testing.T) {
	f := &fakeS3{lie: true}
	_, err := push(t, f, plaintextZip(t), []Recipient{{Name: "offline-a", PublicKey: testKey(t).PublicKey()}})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("got %v, want ErrChecksumMismatch", err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("%d requests: the header must not follow an object that did not verify", len(f.requests))
	}
}

// Without ChecksumSHA256 in the answer, the ETag (single-part MD5) is the
// fallback — and it is the instrument, so it must match too.
func TestPushFallsBackToTheETag(t *testing.T) {
	f := &fakeS3{noSum: true}
	if _, err := push(t, f, plaintextZip(t), []Recipient{{Name: "offline-a", PublicKey: testKey(t).PublicKey()}}); err != nil {
		t.Fatalf("ETag fallback: %v", err)
	}
	if err := checkPut(&s3.PutObjectOutput{ETag: aws.String(`"0123"`)}, "sum", "4567"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("wrong ETag: %v", err)
	}
	if err := checkPut(&s3.PutObjectOutput{}, "sum", "4567"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("no checksum at all: %v", err)
	}
}

func TestParseDest(t *testing.T) {
	d, err := ParseDest("s3://example-hz-backups/hz-instances/loadtest/")
	if err != nil || d.Bucket != "example-hz-backups" || d.Prefix != "hz-instances/loadtest" {
		t.Fatalf("%+v %v", d, err)
	}
	if k := d.ObjectKey("lt box", pushAt); k != "hz-instances/loadtest/lt_box/20261009T040506Z" {
		t.Fatal(k)
	}
	for _, bad := range []string{"example-hz-backups/x", "s3://", "https://example.com/x"} {
		if _, err := ParseDest(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// awsFiles writes a shared config and credentials file and points the SDK at
// them only, with every environment credential cleared and IMDS disabled, so
// nothing here touches the network or the developer's own ~/.aws.
func awsFiles(t *testing.T, cfgText, credText string) []func(*awsconfig.LoadOptions) error {
	t.Helper()
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	dir := t.TempDir()
	c, cr := filepath.Join(dir, "config"), filepath.Join(dir, "credentials")
	if err := os.WriteFile(c, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cr, []byte(credText), 0o600); err != nil {
		t.Fatal(err)
	}
	return []func(*awsconfig.LoadOptions) error{
		awsconfig.WithSharedConfigFiles([]string{c}),
		awsconfig.WithSharedCredentialsFiles([]string{cr}),
	}
}

// The loadtest shape: an assumed role whose source is the instance's own
// metadata credentials, no static key. It must resolve through the same
// LoadAWSConfig as a static key, to an assume-role provider.
func TestAnAssumedRoleProfileResolvesToAssumeRole(t *testing.T) {
	opts := awsFiles(t, `[profile hz-backup]
role_arn = arn:aws:iam::123456789012:role/hz-backup-writer
credential_source = Ec2InstanceMetadata
region = us-west-2
`, "")
	cfg, err := LoadAWSConfig(t.Context(), "", "", opts...)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "us-west-2" {
		t.Fatalf("region %q", cfg.Region)
	}
	cache, ok := cfg.Credentials.(*aws.CredentialsCache)
	if !ok {
		t.Fatalf("credentials are %T", cfg.Credentials)
	}
	if !cache.IsCredentialsProvider(&stscreds.AssumeRoleProvider{}) {
		t.Fatal("an assume-role profile did not resolve to an assume-role provider")
	}
	// Positive control: the same check says no for a static key.
	if cache.IsCredentialsProvider(credentials.StaticCredentialsProvider{}) {
		t.Fatal("instrument: the provider check says yes to everything")
	}
}

// The office shape: a static-key user profile, same code path.
func TestAStaticKeyProfileResolvesToItsKey(t *testing.T) {
	opts := awsFiles(t, "[profile hz-backup]\nregion = us-west-2\n",
		"[hz-backup]\naws_access_key_id = AKIDEXAMPLE\naws_secret_access_key = example-secret\n")
	cfg, err := LoadAWSConfig(t.Context(), "", "", opts...)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "AKIDEXAMPLE" {
		t.Fatalf("%+v %v", creds, err)
	}
	if cfg.Credentials.(*aws.CredentialsCache).IsCredentialsProvider(&stscreds.AssumeRoleProvider{}) {
		t.Fatal("a static key resolved as an assumed role")
	}
}

func TestAProfileWithNoRegionIsRefused(t *testing.T) {
	opts := awsFiles(t, "[profile hz-backup]\n", "[hz-backup]\naws_access_key_id = AKIDEXAMPLE\naws_secret_access_key = x\n")
	if _, err := LoadAWSConfig(t.Context(), "", "", opts...); err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("got %v", err)
	}
	if _, err := LoadAWSConfig(t.Context(), "", "us-west-2", opts...); err != nil {
		t.Fatalf("--region: %v", err)
	}
}

// The bucket policy denies non-TLS. The production client carries no
// endpoint override, and the SDK's own resolution for a real bucket is HTTPS.
func TestTheProductionClientIsTLS(t *testing.T) {
	c := NewS3Client(aws.Config{Region: "us-west-2"})
	if o := c.Options(); o.BaseEndpoint != nil {
		t.Fatalf("production client has an endpoint override: %s", *o.BaseEndpoint)
	}
	ep, err := s3.NewDefaultEndpointResolverV2().ResolveEndpoint(t.Context(), s3.EndpointParameters{
		Bucket: aws.String("example-hz-backups"), Region: aws.String("us-west-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ep.URI.Scheme != "https" {
		t.Fatalf("resolved %s", ep.URI.String())
	}
}
