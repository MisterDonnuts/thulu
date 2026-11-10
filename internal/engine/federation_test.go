package engine

import "testing"

func TestFederationConfig_Validate(t *testing.T) {
	ok := FederationConfig{
		RuleID: "fdrl_01", OrganizationID: "123e4567-e89b-12d3-a456-426614174000", ServiceAccountID: "svac_01",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	withWS := ok
	withWS.WorkspaceID = "wrkspc_01"
	if err := withWS.Validate(); err != nil {
		t.Errorf("workspace refused: %v", err)
	}
	for name, mut := range map[string]func(*FederationConfig){
		"rule prefix":        func(c *FederationConfig) { c.RuleID = "rule_01" },
		"bare rule prefix":   func(c *FederationConfig) { c.RuleID = "fdrl_" },
		"org not a uuid":     func(c *FederationConfig) { c.OrganizationID = "org_01" },
		"no service account": func(c *FederationConfig) { c.ServiceAccountID = "" },
		"workspace prefix":   func(c *FederationConfig) { c.WorkspaceID = "ws_01" },
	} {
		c := ok
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, c)
		}
	}
}

func TestFederationConfig_EncodeDecode(t *testing.T) {
	if (FederationConfig{}).Encode() != "" {
		t.Error("an unset config must encode to empty")
	}
	c := FederationConfig{RuleID: "fdrl_1", OrganizationID: "o", ServiceAccountID: "svac_1", WorkspaceID: "wrkspc_1"}
	got, err := DecodeFederation(c.Encode())
	if err != nil || got != c {
		t.Errorf("round trip = %+v, %v", got, err)
	}
	if got, err := DecodeFederation(""); err != nil || got.Set() {
		t.Errorf("empty decodes to %+v, %v", got, err)
	}
	if _, err := DecodeFederation("{"); err == nil {
		t.Error("bad JSON decoded")
	}
}
