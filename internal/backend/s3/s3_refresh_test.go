package s3

import (
	"fmt"
	"testing"
	"time"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

type mockProvider struct {
	calls      int
	expiration time.Time
}

func (m *mockProvider) Retrieve() (credentials.Value, error) {
	m.calls++
	m.expiration = time.Now().Add(2 * time.Second)
	return credentials.Value{
		AccessKeyID:     fmt.Sprintf("access%d", m.calls),
		SecretAccessKey: "secret",
		SignerType:      credentials.SignatureV4,
	}, nil
}

func (m *mockProvider) IsExpired() bool {
	return time.Now().After(m.expiration)
}

func TestCredentialsRefresh(t *testing.T) {
	provider := &mockProvider{}
	creds := credentials.New(provider)

	val, err := creds.Get()
	if err != nil {
		(t).Fatal(err)
	}
	if val.AccessKeyID != "access1" {
		(t).Fatalf("expected access1, got %s", val.AccessKeyID)
	}
	if provider.calls != 1 {
		(t).Fatalf("expected 1 call, got %d", provider.calls)
	}

	// Should not refresh immediately
	val, err = creds.Get()
	if err != nil {
		(t).Fatal(err)
	}
	if val.AccessKeyID != "access1" {
		(t).Fatalf("expected access1, got %s", val.AccessKeyID)
	}
	if provider.calls != 1 {
		(t).Fatalf("expected 1 call, got %d", provider.calls)
	}

	// Wait for TTL to expire
	time.Sleep(2100 * time.Millisecond)

	// Should refresh now
	val, err = creds.Get()
	if err != nil {
		(t).Fatal(err)
	}
	if val.AccessKeyID != "access2" {
		(t).Fatalf("expected access2, got %s", val.AccessKeyID)
	}
	if provider.calls != 2 {
		(t).Fatalf("expected 2 calls, got %d", provider.calls)
	}
}
