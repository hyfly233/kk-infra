package artifact

import "testing"

func TestParseS3URI(t *testing.T) {
	bucket, object, err := ParseS3URI("s3://models/qwen/weights.bin")
	if err != nil || bucket != "models" || object != "qwen/weights.bin" {
		t.Fatalf("unexpected parse result: %q %q %v", bucket, object, err)
	}
	for _, uri := range []string{"https://example.com/model", "s3://models", "s3:///object", "s3://user:secret@models/object", "s3://models/object?token=secret", "s3://models/object#secret", "s3://models:9000/object"} {
		if _, _, err := ParseS3URI(uri); err == nil {
			t.Errorf("expected invalid URI: %s", uri)
		}
	}
}
