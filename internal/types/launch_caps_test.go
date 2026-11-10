package types

import "testing"

func TestApplyResourceCapsZeroIsUnbounded(t *testing.T) {
	hc, err := buildHostConfig(&ComposeServiceSpec{}, nil, ResourceCaps{})
	if err != nil {
		t.Fatal(err)
	}
	if hc.Resources.Memory != 0 || hc.Resources.MemorySwap != 0 ||
		hc.Resources.NanoCPUs != 0 || hc.Resources.PidsLimit != nil {
		t.Fatalf("zero caps must leave resources unset, got %+v", hc.Resources)
	}
}

func TestApplyResourceCapsMemoryPinsSwap(t *testing.T) {
	const mem = int64(1) << 30
	hc, err := buildHostConfig(&ComposeServiceSpec{}, nil, ResourceCaps{MemoryBytes: mem})
	if err != nil {
		t.Fatal(err)
	}
	if hc.Resources.Memory != mem {
		t.Errorf("Memory = %d, want %d", hc.Resources.Memory, mem)
	}
	if hc.Resources.MemorySwap != mem {
		t.Errorf("MemorySwap = %d, want %d (pinned to Memory)", hc.Resources.MemorySwap, mem)
	}
}

func TestApplyResourceCapsPidsAndCPU(t *testing.T) {
	hc, err := buildHostConfig(&ComposeServiceSpec{}, nil, ResourceCaps{PidsLimit: 512, NanoCPUs: 1_500_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if hc.Resources.PidsLimit == nil || *hc.Resources.PidsLimit != 512 {
		t.Errorf("PidsLimit = %v, want 512", hc.Resources.PidsLimit)
	}
	if hc.Resources.NanoCPUs != 1_500_000_000 {
		t.Errorf("NanoCPUs = %d, want 1.5e9", hc.Resources.NanoCPUs)
	}
}

func TestBuildHostConfigKeepsSecurityOpt(t *testing.T) {
	hc, err := buildHostConfig(&ComposeServiceSpec{}, nil, ResourceCaps{MemoryBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range hc.SecurityOpt {
		if o == "no-new-privileges:true" {
			found = true
		}
	}
	if !found {
		t.Errorf("SecurityOpt lost no-new-privileges: %v", hc.SecurityOpt)
	}
}
