package iridium

import (
	"slices"
	"testing"
)

func TestKnownProductsHandsOutACopy(t *testing.T) {
	products := KnownProducts()
	products[0].Model = "changed"

	if again := KnownProducts(); again[0].Model != "Iridium 9555" {
		t.Errorf("KnownProducts after a change to its result = %+v", again)
	}
}

func TestKnownProductsAreTheDriversInItsOrder(t *testing.T) {
	want := []Product{
		{ProductID: 0x9555, Model: "Iridium 9555"},
		{ProductID: 0xA555, Model: "Iridium 9555A"},
		{ProductID: 0x9575, Model: "Iridium 9575"},
		{ProductID: 0x5054, Model: "Iridium 9575P"},
		{ProductID: 0x7545, Model: "Iridium 9575E"},
		{ProductID: 0x520C, Model: "Iridium ROC (service port)"},
	}

	if got := KnownProducts(); !slices.Equal(got, want) {
		t.Errorf("KnownProducts = %+v, want %+v", got, want)
	}
}

func TestModelForProductID(t *testing.T) {
	if model, ok := ModelForProductID(0x9575); !ok || model != "Iridium 9575" {
		t.Errorf("ModelForProductID(0x9575) = %q, %v", model, ok)
	}
	if model, ok := ModelForProductID(0x1234); ok || model != "" {
		t.Errorf("ModelForProductID(0x1234) = %q, %v; want unknown", model, ok)
	}
}
