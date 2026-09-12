package validate

import (
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	if err := Rate(1); err != nil {
		t.Fatalf("expected no error for positive rate, got %v", err)
	}
	if err := Rate(0); err == nil {
		t.Fatal("expected error for zero rate")
	}
	if err := Rate(-1); err == nil {
		t.Fatal("expected error for negative rate")
	}
}

func TestWindow(t *testing.T) {
	if err := Window(time.Second); err != nil {
		t.Fatalf("expected no error for positive window, got %v", err)
	}
	if err := Window(0); err == nil {
		t.Fatal("expected error for zero window")
	}
	if err := Window(-time.Second); err == nil {
		t.Fatal("expected error for negative window")
	}
}

func TestLimit(t *testing.T) {
	if err := Limit(1); err != nil {
		t.Fatalf("expected no error for positive limit, got %v", err)
	}
	if err := Limit(0); err == nil {
		t.Fatal("expected error for zero limit")
	}
	if err := Limit(-1); err == nil {
		t.Fatal("expected error for negative limit")
	}
}
