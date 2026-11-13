package catalog

import "testing"

func TestBuildIsDeterministicAndEncodesSellers(t *testing.T) {
	spec := Spec{Sellers: 50, Products: 2000, Users: 1000, ImageShare: 0.1}
	a, b := Build(spec), Build(spec)
	if len(a.Items) != spec.Products {
		t.Fatalf("got %d products", len(a.Items))
	}
	for i := range a.Items {
		if a.Items[i] != b.Items[i] {
			t.Fatalf("item %d differs between builds", i)
		}
		it := a.Items[i]
		if SellerOf(it.ID) != it.SellerID {
			t.Fatalf("product %d does not encode seller %d", it.ID, it.SellerID)
		}
		if it.ID%ProductIDBase >= FirstRunLocal {
			t.Fatalf("base product %d numbers into the run range", it.ID)
		}
		if got, ok := a.Lookup(it.ID); !ok || got != it {
			t.Fatalf("lookup of %d failed", it.ID)
		}
	}
	if a.Full(a.Items[10]).Description != b.Full(b.Items[10]).Description {
		t.Fatal("full product text is not deterministic")
	}
}

func TestVariantIDsStayWithinTheSeller(t *testing.T) {
	product := int64(1234)*ProductIDBase + 42
	for k := 1; k <= 9; k++ {
		if VariantID(product, k)/10_000_000 != 1234 {
			t.Fatalf("variant %d of %d escapes its seller", k, product)
		}
	}
}

func TestEveryCategoryHasProducts(t *testing.T) {
	ix := Build(Default)
	for _, c := range ix.Categories {
		if len(c.Products) == 0 {
			t.Fatalf("category %s is empty", c.Slug)
		}
	}
	if len(ix.Categories) != 38 {
		t.Fatalf("got %d categories, the plan's narration assumes 38", len(ix.Categories))
	}
}
