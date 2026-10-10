package awsapi

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorQueryStyle(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, StyleQuery, http.StatusNotFound, "NoSuchEntity", "profile <x> not found")
	var got struct {
		Error     struct{ Type, Code, Message string }
		RequestId string
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not valid XML: %v\n%s", err, rec.Body)
	}
	if rec.Code != 404 || got.Error.Code != "NoSuchEntity" || got.Error.Message != "profile <x> not found" || got.RequestId == "" {
		t.Fatalf("status=%d body=%+v", rec.Code, got)
	}
}

func TestWriteErrorEC2Style(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, StyleEC2, http.StatusBadRequest, "InsufficientInstanceCapacity", "no capacity")
	var got struct {
		Errors struct {
			Error struct{ Code, Message string }
		}
		RequestID string
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not valid XML: %v\n%s", err, rec.Body)
	}
	if got.Errors.Error.Code != "InsufficientInstanceCapacity" || got.RequestID == "" {
		t.Fatalf("body=%+v", got)
	}
}

func TestWriteErrorJSONAndRESTJSONStyles(t *testing.T) {
	for _, s := range []Style{StyleJSON, StyleRESTJSON} {
		rec := httptest.NewRecorder()
		WriteError(rec, s, http.StatusBadRequest, "ResourceNotFoundException", "no such cluster")
		if rec.Header().Get("X-Amzn-Errortype") != "ResourceNotFoundException" {
			t.Errorf("%s: missing X-Amzn-Errortype header", s)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["message"] != "no such cluster" {
			t.Errorf("%s: body=%s err=%v", s, rec.Body, err)
		}
	}
}

func TestNewRequestIDLooksLikeAUUIDAndDiffers(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if len(a) != 36 || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
}
