package awsapi

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// signedTargetReq builds a JSON-RPC style request carrying an X-Amz-Target and signs it.
func signedTargetReq(t *testing.T, target string) (*http.Request, []byte, time.Time) {
	t.Helper()
	body := []byte("{}")
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r, err := http.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	if target != "" {
		r.Header.Set("X-Amz-Target", target)
	}
	Sign(r, body, "AKIDEXAMPLE", "secret-for-test", "us-west-2", "sqs", now)
	return r, body, now
}

func lookupTest(ak string) (string, bool) { return "secret-for-test", ak == "AKIDEXAMPLE" }

// X-Amz-Target selects the operation, so Sign must cover it with the signature.
func TestSignCoversXAmzTargetAndVerifyAccepts(t *testing.T) {
	r, body, now := signedTargetReq(t, "AmazonSQS.GetQueueUrl")
	a, err := Verify(r, body, lookupTest, now, time.Minute)
	if err != nil {
		t.Fatalf("signed target rejected: %v", err)
	}
	if !slices.Contains(a.SignedHeaders, "x-amz-target") {
		t.Fatalf("x-amz-target not in SignedHeaders: %v", a.SignedHeaders)
	}
}

// Swapping the target after signing must fail the signature check.
func TestVerifyRejectsSwappedXAmzTarget(t *testing.T) {
	r, body, now := signedTargetReq(t, "AmazonSQS.GetQueueUrl")
	r.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	if _, err := Verify(r, body, lookupTest, now, time.Minute); err == nil {
		t.Fatal("swapped X-Amz-Target accepted")
	}
}

// A target added to a request whose signature does not list it is refused outright.
func TestVerifyRejectsXAmzTargetThatIsNotSigned(t *testing.T) {
	r, body, now := signedTargetReq(t, "") // signed without a target
	r.Header.Set("X-Amz-Target", "AmazonSQS.CreateQueue")
	if _, err := Verify(r, body, lookupTest, now, time.Minute); err != ErrInvalidAuth {
		t.Fatalf("err = %v, want ErrInvalidAuth", err)
	}
}
