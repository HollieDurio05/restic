package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/restic/restic/internal/backend"
	"github.com/restic/restic/internal/debug"
	"github.com/restic/restic/internal/errors"
)

// Backend stores data on an S3 compatible storage.
type Backend struct {
	client *minio.Client
	cfg    Config
	backend.Layout
}

// Open opens the S3 backend at bucket and prefix.
func Open(ctx context.Context, cfg Config, rt http.RoundTripper) (*Backend, error) {
	debug.Log("open roadtripper %p", rt)

	var creds *credentials.Credentials
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken)
	} else {
		var providers []credentials.Provider
		providers = append(providers, &credentials.EnvAWS{})
		providers = append(providers, &credentials.EnvMinio{})
		if tokenFile := os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"); tokenFile != "" {
			roleARN := os.Getenv("AWS_ROLE_ARN")
			roleSessionName := os.Getenv("AWS_ROLE_SESSION_NAME")
			if roleSessionName == "" {
				roleSessionName = "restic-session"
			}
			providers = append(providers, &credentials.STSWebIdentity{
				Client: &http.Client{
					Transport: http.DefaultTransport,
				},
				TokenFile:       tokenFile,
				RoleARN:         roleARN,
				RoleSessionName: roleSessionName,
			})
		}
		providers = append(providers, &credentials.IAM{
			Client: &http.Client{
				Transport: http.DefaultTransport,
			},
		})
		creds = credentials.NewChainCredentials(providers)
	}

	var bucketLookup minio.BucketLookupType
	switch cfg.BucketLookup {
	case "auto":
		bucketLookup = minio.BucketLookupAuto
	case "dns":
		bucketLookup = minio.BucketLookupDNS
	case "path":
		bucketLookup = minio.BucketLookupPath
	case "":
		bucketLookup = minio.BucketLookupAuto
	default:
		return nil, errors.Errorf("invalid bucket lookup type %q", cfg.BucketLookup)
	}

	options := &minio.Options{
		Creds:        creds,
		Secure:       !cfg.UseHTTP,
		Transport:    rt,
		Region:       cfg.Region,
		BucketLookup: bucketLookup,
	}

	client, err := minio.New(cfg.Endpoint, options)
	if err != nil {
		return nil, errors.Wrap(err, "minio.New")
	}

	be := &Backend{
		client: client,
		cfg:    cfg,
	}

	l, err := backend.SelectLayout(ctx, be, cfg.Layout, cfg.Prefix)
	if err != nil {
		return nil, err
	}

	be.Layout = l

	return be, nil
}

// IsNotExist returns true if the error is caused by a non-existing file.
func (be *Backend) IsNotExist(err error) bool {
	if err == nil {
		return false
	}

	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return true
	}

	return false
}

// Join joins multiple paths.
func (be *Backend) Join(p ...string) string {
	return strings.Join(p, "/")
}

// Location returns the location of the backend.
func (be *Backend) Location() string {
	return be.Join(be.cfg.Bucket, be.cfg.Prefix)
}

// Hashes returns the list of supported hash algorithms.
func (be *Backend) Hashes() []string {
	return []string{"sha256"}
}

// Save stores data in the backend.
func (be *Backend) Save(ctx context.Context, h backend.Handle, rd backend.RewindReader) error {
	if err := h.Valid(); err != nil {
		return err
	}

	objName := be.Filename(h)

	opts := minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	}

	if be.cfg.StorageClass != "" {
		opts.StorageClass = be.cfg.StorageClass
	}

	_, err := be.client.PutObject(ctx, be.cfg.Bucket, objName, rd, rd.Length(), opts)
	if err != nil {
		return errors.Wrap(err, "client.PutObject")
	}

	return nil
}

// Load runs fn with a reader that yields the contents of the file at h.
func (be *Backend) Load(ctx context.Context, h backend.Handle, length int, offset int64, fn func(rd io.Reader) error) error {
	if err := h.Valid(); err != nil {
		return err
	}

	if offset < 0 {
		return errors.New("offset must be >= 0")
	}

	if length < 0 {
		return errors.New("length must be >= 0")
	}

	objName := be.Filename(h)

	opts := minio.GetObjectOptions{}
	if length > 0 || offset > 0 {
		var err error
		if length > 0 {
			err = opts.SetRange(offset, offset+int64(length)-1)
		} else {
			err = opts.SetRange(offset, 0)
		}
		if err != nil {
			return errors.Wrap(err, "SetRange")
		}
	}

	coreClient := minio.Core{Client: be.client}
	rd, _, _, err := coreClient.GetObject(ctx, be.cfg.Bucket, objName, opts)
	if err != nil {
		return errors.Wrap(err, "GetObject")
	}

	defer func() {
		_ = rd.Close()
	}()

	return fn(rd)
}

// Stat returns information about a file.
func (be *Backend) Stat(ctx context.Context, h backend.Handle) (backend.FileInfo, error) {
	if err := h.Valid(); err != nil {
		return backend.FileInfo{}, err
	}

	objName := be.Filename(h)

	opts := minio.StatObjectOptions{}
	fi, err := be.client.StatObject(ctx, be.cfg.Bucket, objName, opts)
	if err != nil {
		return backend.FileInfo{}, errors.Wrap(err, "client.StatObject")
	}

	return backend.FileInfo{
		Size: fi.Size,
		Name: h.Name,
	}, nil
}

// Remove removes a file.
func (be *Backend) Remove(ctx context.Context, h backend.Handle) error {
	if err := h.Valid(); err != nil {
		return err
	}

	objName := be.Filename(h)

	opts := minio.RemoveObjectOptions{}
	err := be.client.RemoveObject(ctx, be.cfg.Bucket, objName, opts)
	if err != nil {
		return errors.Wrap(err, "client.RemoveObject")
	}

	return nil
}

// List runs fn for each file in the backend.
func (be *Backend) List(ctx context.Context, t backend.Type, fn func(backend.FileInfo) error) error {
	prefix := be.Dirname(backend.Handle{Type: t}) + "/"

	opts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
		UseV1:     be.cfg.ListObjectsV1,
	}

	for obj := range be.client.ListObjects(ctx, be.cfg.Bucket, opts) {
		if obj.Err != nil {
			return errors.Wrap(obj.Err, "client.ListObjects")
		}

		name := obj.Key
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		name = name[len(prefix):]
		if name == "" {
			continue
		}

		fi := backend.FileInfo{
			Name: name,
			Size: obj.Size,
		}

		err := fn(fi)
		if err != nil {
			return err
		}
	}

	return nil
}

// Close closes the backend.
func (be *Backend) Close() error {
	return nil
}
