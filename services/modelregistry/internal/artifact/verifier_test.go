package artifact

import "testing"

func TestParseS3URI(t *testing.T) {
	bucket, object, err := ParseS3URI("s3://models/qwen/weights.bin")
	if err != nil || bucket != "models" || object != "qwen/weights.bin" {
		t.Fatalf("unexpected parse result: %q %q %v", bucket, object, err)
	}
	for _, uri := range []string{"https://example.com/model", "s3://models", "s3:///object"} {
		if _, _, err := ParseS3URI(uri); err == nil {
			t.Errorf("expected invalid URI: %s", uri)
		}
	}
}
