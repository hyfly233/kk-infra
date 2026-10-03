package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Metadata struct {
	Digest     string
	Size       int64
	VerifiedAt time.Time
}

type Verifier interface {
	Verify(context.Context, string, string) (*Metadata, error)
}

type S3Verifier struct{ client *minio.Client }

func NewS3Verifier(endpoint, accessKey, secretKey string, secure bool) (*S3Verifier, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, err
	}
	return &S3Verifier{client: client}, nil
}

func (v *S3Verifier) Verify(ctx context.Context, rawURI, expectedDigest string) (*Metadata, error) {
	bucket, object, err := ParseS3URI(rawURI)
	if err != nil {
		return nil, err
	}
	info, err := v.client.StatObject(ctx, bucket, object, minio.StatObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("S3 HeadObject 失败: %w", err)
	}
	digest := strings.TrimSpace(info.Metadata.Get("X-Amz-Meta-Sha256"))
	if digest == "" {
		return nil, fmt.Errorf("artifact 缺少 x-amz-meta-sha256 元数据")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}
	if expectedDigest != "" && !strings.EqualFold(expectedDigest, digest) {
		return nil, fmt.Errorf("artifact checksum 不匹配: expected=%s actual=%s", expectedDigest, digest)
	}
	return &Metadata{Digest: digest, Size: info.Size, VerifiedAt: time.Now().UTC()}, nil
}

func ParseS3URI(rawURI string) (string, string, error) {
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "s3" || u.Host == "" || strings.TrimPrefix(u.Path, "/") == "" {
		return "", "", fmt.Errorf("artifactUri 必须为 s3://bucket/object")
	}
	return u.Host, strings.TrimPrefix(u.Path, "/"), nil
}

// DevelopmentVerifier keeps local fake-cluster workflows deterministic without
// claiming that a remote object was checked. Production should configure S3Verifier.
type DevelopmentVerifier struct{}

func (DevelopmentVerifier) Verify(_ context.Context, rawURI, expectedDigest string) (*Metadata, error) {
	if _, _, err := ParseS3URI(rawURI); err != nil {
		return nil, err
	}
	digest := expectedDigest
	if digest == "" {
		sum := sha256.Sum256([]byte(rawURI))
		digest = "dev-sha256:" + hex.EncodeToString(sum[:])
	}
	return &Metadata{Digest: digest, VerifiedAt: time.Now().UTC()}, nil
}
