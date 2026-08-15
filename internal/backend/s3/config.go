package s3

import (
	"net/url"
	"os"
	"strings"

	"github.com/restic/restic/internal/errors"
	"github.com/restic/restic/internal/options"
)

// Config contains all configuration needed for the S3 backend.
type Config struct {
	Endpoint     string
	Bucket       string
	Prefix       string

	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string

	Region           string
	BucketLookup     string
	Layout           string
	ListObjectsV1    bool
	LegacyLayout     bool
	NoConnectionPool bool
	StorageClass     string

	Connections uint
}

func init() {
	var register = func(s string) {
		options.Register("s3", s)
	}

	register("key-id")
	register("secret")
	register("session-token")
	register("region")
	register("bucket-lookup")
	register("layout")
	register("list-objects-v1")
	register("legacy-layout")
	register("no-connection-pool")
	register("storage-class")
	register("connections")
}

// ParseConfig parses the string s and returns a Config.
func ParseConfig(s string) (interface{}, error) {
	if !strings.HasPrefix(s, "s3:") {
		return nil, errors.Errorf("invalid s3 backend specification: %q", s)
	}

	s = s[3:]
	cfg := Config{
		Connections: 5,
	}

	if strings.HasPrefix(s, "//") {
		s = s[2:]
	}

	// parse endpoint and bucket/prefix
	// s3:http://endpoint/bucket/prefix
	// s3:endpoint/bucket/prefix
	var endpoint, path string
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, errors.Wrap(err, "url.Parse")
		}
		endpoint = u.Scheme + "://" + u.Host
		path = u.Path
	} else {
		p := strings.SplitN(s, "/", 2)
		endpoint = p[0]
		if len(p) > 1 {
			path = "/" + p[1]
		}
	}

	cfg.Endpoint = endpoint

	path = strings.TrimPrefix(path, "/")
	p := strings.SplitN(path, "/", 2)
	cfg.Bucket = p[0]
	if len(p) > 1 {
		cfg.Prefix = p[1]
	}

	if cfg.Bucket == "" {
		return nil, errors.New("no bucket name specified")
	}

	// apply environment variables
	if cfg.Region == "" {
		cfg.Region = os.Getenv("AWS_DEFAULT_REGION")
	}

	return cfg, nil
}
