package amdsmi

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fixture = `[
  {
    "bdf": "0000:03:00.0",
    "uuid": "abc-123",
    "asic": {
      "market_name": "Radeon RX 7900 XTX",
      "asic_serial": "ABCDEF12",
      "device_id": "0x744c"
    },
    "vbios": {
      "version": "113-D70200-100",
      "date": "2023-08-01"
    }
  }
]`

func writeStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "amd-smi-stub")
	script := "#!/bin/sh\ncat <<'JSON'\n" + body + "\nJSON\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	return path
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFetchStaticHappyPath(t *testing.T) {
	stub := writeStub(t, fixture)
	b := New(stub, time.Second)
	got := b.FetchStatic(context.Background(), quietLogger())
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	s, ok := got["0000:03:00.0"]
	if !ok {
		t.Fatalf("missing bdf, got keys: %v", got)
	}
	if s.UUID != "abc-123" {
		t.Errorf("uuid = %q", s.UUID)
	}
	if s.MarketName != "Radeon RX 7900 XTX" {
		t.Errorf("market_name = %q", s.MarketName)
	}
	if s.VBIOSDate != "2023-08-01" {
		t.Errorf("vbios_date = %q", s.VBIOSDate)
	}
}

func TestFetchStaticSingleObjectShape(t *testing.T) {
	// Older amd-smi releases emit a single object instead of an array.
	stub := writeStub(t, `{"bdf":"0000:03:00.0","uuid":"u"}`)
	b := New(stub, time.Second)
	got := b.FetchStatic(context.Background(), quietLogger())
	if got["0000:03:00.0"].UUID != "u" {
		t.Errorf("uuid = %q", got["0000:03:00.0"].UUID)
	}
}

func TestFetchStaticMissingBinary(t *testing.T) {
	b := New("/nonexistent/amd-smi", time.Second)
	got := b.FetchStatic(context.Background(), quietLogger())
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestFetchStaticMalformedJSON(t *testing.T) {
	stub := writeStub(t, "not json at all")
	b := New(stub, time.Second)
	got := b.FetchStatic(context.Background(), quietLogger())
	if len(got) != 0 {
		t.Errorf("expected empty map on parse error, got %v", got)
	}
}

func TestFetchStaticDropsNAValues(t *testing.T) {
	stub := writeStub(t, `[{"bdf":"0000:03:00.0","uuid":"N/A","asic":{"market_name":""}}]`)
	b := New(stub, time.Second)
	got := b.FetchStatic(context.Background(), quietLogger())
	s := got["0000:03:00.0"]
	if s.UUID != "" || s.MarketName != "" {
		t.Errorf("expected empty fields, got %+v", s)
	}
}
