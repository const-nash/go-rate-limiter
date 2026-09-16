package validate_test

import (
	"testing"
	"time"

	"github.com/const-nash/go-rate-limiter/internal/validate"
)

func TestRate(t *testing.T) {
	if err := validate.Rate(1); err != nil {
		t.Fatalf("expected no error for positive rate, got %v", err)
	}
	if err := validate.Rate(0); err == nil {
		t.Fatal("expected error for zero rate")
	}
	if err := validate.Rate(-1); err == nil {
		t.Fatal("expected error for negative rate")
	}
}

func TestWindow(t *testing.T) {
	if err := validate.Window(time.Second); err != nil {
		t.Fatalf("expected no error for positive window, got %v", err)
	}
	if err := validate.Window(0); err == nil {
		t.Fatal("expected error for zero window")
	}
	if err := validate.Window(-time.Second); err == nil {
		t.Fatal("expected error for negative window")
	}
}

func TestCleanupInterval(t *testing.T) {
	if err := validate.CleanupInterval(time.Second); err != nil {
		t.Fatalf("expected no error for positive interval, got %v", err)
	}
	if err := validate.CleanupInterval(0); err == nil {
		t.Fatal("expected error for zero interval")
	}
	if err := validate.CleanupInterval(-time.Second); err == nil {
		t.Fatal("expected error for negative interval")
	}
}

func TestLimit(t *testing.T) {
	if err := validate.Limit(1); err != nil {
		t.Fatalf("expected no error for positive limit, got %v", err)
	}
	if err := validate.Limit(0); err == nil {
		t.Fatal("expected error for zero limit")
	}
	if err := validate.Limit(-1); err == nil {
		t.Fatal("expected error for negative limit")
	}
}
