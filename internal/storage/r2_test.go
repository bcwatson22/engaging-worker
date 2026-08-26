package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestEndpoint(t *testing.T) {
	want := "https://acct123.r2.cloudflarestorage.com"
	if got := Endpoint("acct123"); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestNewUsesTheAccountEndpoint(t *testing.T) {
	c := New("acct", "key", "secret", "bucket", "https://pub.r2.dev")
	if c.bucket != "bucket" || c.publicBase != "https://pub.r2.dev" {
		t.Errorf("unexpected client: %+v", c)
	}
	if c.s3 == nil {
		t.Error("expected an S3 client")
	}
}

// setup points a client at a test server so the request R2 would receive can
// be asserted without reaching Cloudflare.
func setup(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &Client{
		s3: s3.New(s3.Options{
			Region:       region,
			BaseEndpoint: aws.String(server.URL),
			Credentials:  credentials.NewStaticCredentialsProvider("key", "secret", ""),
			UsePathStyle: true,
		}),
		bucket:     "artifacts",
		publicBase: "https://pub.r2.dev",
	}, server
}

func TestUploadSendsTheObjectAndItsHeaders(t *testing.T) {
	var gotPath, gotType, gotCache string
	var gotBody []byte

	client, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		gotCache = r.Header.Get("Cache-Control")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = buf
		w.WriteHeader(http.StatusOK)
	})

	url, err := client.Upload(context.Background(), "candidate/cv.pdf",
		[]byte("%PDF-1.4"), "application/pdf", "public, max-age=600")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasSuffix(gotPath, "/artifacts/candidate/cv.pdf") {
		t.Errorf("path: got %q", gotPath)
	}
	if gotType != "application/pdf" {
		t.Errorf("content type: got %q", gotType)
	}
	// Without one, Vercel proxies every request straight through to the bucket.
	if gotCache != "public, max-age=600" {
		t.Errorf("cache control: got %q", gotCache)
	}
	if string(gotBody) != "%PDF-1.4" {
		t.Errorf("body: got %q", gotBody)
	}
	if url != "https://pub.r2.dev/candidate/cv.pdf" {
		t.Errorf("should return the public URL, got %q", url)
	}
}

func TestUploadReportsFailures(t *testing.T) {
	client, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := client.Upload(context.Background(), "k", []byte("x"), "text/plain", "no-store")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "uploading k") {
		t.Errorf("error should name the key: %v", err)
	}
}
