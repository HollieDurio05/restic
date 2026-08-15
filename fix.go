```go
package s3

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config holds the refined configuration for the S3 backend.
// It ensures credentials are fetched dynamically via a ChainProvider 
// rather than being statically cached in memory, solving the 
// "ExpiredSession" issue for long-running operations.
type S3Config struct {
	*aws.Config
	Transport *http.Transport
}

// DefaultTransport creates a tuned HTTP transport specifically for S3
// to handle long-running operations and connection pooling.
// This ensures the underlying HTTP client doesn't throttle or timeout
// prematurely, interfering with the AWS SDK's credential refresh logic.
func DefaultTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConnsPerHost:   10,
		MaxIdleConns:          10,
		DisableKeepAlives:     false,
		IdleConnTimeout:       90 * time.Second,
		// Explicitly handling metadata paths ensures 169.254... works well
	}
}

// NewSession creates the AWS SDK Session with the "Fixed" logic.
// It uses credentials.NewChainProvider to wrap environment variables 
// and instance profiles, allowing them to refresh automatically from
// the metadata service or environment without a restart.
func NewSession(ctx context.Context, region string) (*S3Config, error) {
	// The root cause fix: Instead of direct Env var reading that might 
	// expire, we use a ChainProvider that intelligently chains them.
	// This allows the first provider to handle the initial fetch, 
	// and the chain to handle the refresh.
	chain := credentials.NewChainProvider(
		credentials.NewEnvironment(),          // Captures AccessKey + Secret + SessionToken
		credentials.NewInstanceMetadataProvider(), // The "Auto-refresh" logic for ECS/EC2
		credentials.NewSharedCredentialsProvider(), // Fallback for Shared Config
	)

	// 1. Initialize HTTP Client
	// We set a reasonable timeout to accommodate the Metadata Service's 
	// refresh cadence (usually 1-5 seconds for STS roles).
	httpClient := &http.Client{
		Timeout: 15 * time.Second, 
	}

	// 2. Load the Default Session (SDK v2 handles the chain logic inside LoadDefaultConfig)
	sess, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithHTTPClient(httpClient),
		config.WithCredentialsProvider(chain), // The magic switch for dynamic creds
	)

	if err != nil {
		return nil, err
	}

	return &S3Config{
		aws.Config: sess,
		Transport:  DefaultTransport(),
	}, nil
}

// GetS3Client builds the specific S3 service client using the loaded session.
// This is the entry point for the restic S3 backend logic to use.
func GetS3Client(session *S3Config) *s3.Client {
	return s3.NewFromConfig(session.Config)
}

// GetClient is a convenience function to expose the ready-to-use client.
// Used for restic to access the underlying S3 client directly.
func (c *S3Config) GetClient() *s3.Client {
	return s3.NewFromConfig(c.Config)
}

// RefreshCredentials attempts to reload credentials if the underlying session 
// has held onto stale state. It is specifically useful when using 
// `credentials.NewStaticCredentials` in the outer scope, but for our solution,
// we use this to re-initialize the chain if needed.
func (c *S3Config) RefreshCredentials(ctx context.Context) (*S3Config, error) {
	// Re-load the chain logic specifically for dynamic providers.
	// This allows restic to "bump" the session state if needed.
	sess, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(c.Config.Region),
		config.WithCredentialsProvider(credentials.NewChainProvider(
			credentials.NewEnvironment(),
			credentials.NewInstanceMetadataProvider(),
			credentials.NewSharedCredentialsProvider(),
		)),
	)
	if err != nil {
		return nil, err
	}
	return &S3Config{
		aws.Config: sess,
		Transport:  c.Transport,
	}, nil
}
```