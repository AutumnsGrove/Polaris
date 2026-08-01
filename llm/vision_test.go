package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescribeImage_ReturnsDescriptionAndCost(t *testing.T) {
	var captured visionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshaling request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"A red bicycle leaning against a brick wall."}}],"usage":{"cost":0.0021}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "xiaomi/mimo-v2.5", 0.4, 1000)
	desc, cost, err := client.DescribeImage(context.Background(), "ZmFrZS1pbWFnZS1ieXRlcw==", "image/jpeg")
	if err != nil {
		t.Fatalf("DescribeImage returned error: %v", err)
	}
	if desc != "A red bicycle leaning against a brick wall." {
		t.Errorf("description = %q, want the model's content", desc)
	}
	if cost != 0.0021 {
		t.Errorf("cost = %v, want 0.0021", cost)
	}

	if captured.Model != "xiaomi/mimo-v2.5" {
		t.Errorf("request model = %q, want %q", captured.Model, "xiaomi/mimo-v2.5")
	}
	if captured.Stream {
		t.Error("request Stream = true, want false (this is a single blocking call, not SSE)")
	}
	if len(captured.Messages) != 1 || len(captured.Messages[0].Content) != 2 {
		t.Fatalf("request messages = %+v, want one message with a text block and an image_url block", captured.Messages)
	}
	imgBlock := captured.Messages[0].Content[1]
	if imgBlock.Type != "image_url" || imgBlock.ImageURL == nil {
		t.Fatalf("second content block = %+v, want an image_url block", imgBlock)
	}
	if !strings.HasPrefix(imgBlock.ImageURL.URL, "data:image/jpeg;base64,") {
		t.Errorf("image_url = %q, want a data: URL with the right mime type", imgBlock.ImageURL.URL)
	}
}

func TestDescribeImage_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"message":"upstream error"}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "xiaomi/mimo-v2.5", 0.4, 1000)
	_, _, err := client.DescribeImage(context.Background(), "ZmFrZQ==", "image/png")
	if err == nil {
		t.Fatal("expected an error for a 502 response")
	}
}

func TestDescribeImage_EmptyChoicesIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "test-key", "xiaomi/mimo-v2.5", 0.4, 1000)
	_, _, err := client.DescribeImage(context.Background(), "ZmFrZQ==", "image/png")
	if err == nil {
		t.Fatal("expected an error when the model returns no choices")
	}
}
