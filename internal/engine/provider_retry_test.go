package engine

import "testing"

func TestProviderRetryClassification(t *testing.T) {
	if retryableProviderError(&ProviderHTTPError{StatusCode: 400}) {
		t.Fatal("400 must not retry")
	}
	if retryableProviderError(&ProviderHTTPError{StatusCode: 401}) {
		t.Fatal("401 must not retry")
	}
	if !retryableProviderError(&ProviderHTTPError{StatusCode: 429}) || !retryableProviderError(&ProviderHTTPError{StatusCode: 503}) {
		t.Fatal("rate limit and server errors must retry")
	}
}
