package web

import (
	"slices"
	"testing"
)

func TestCustomerType(t *testing.T) {
	for profile, want := range map[string]string{
		"elec-dpn":                  CustomerElec,
		"elec-24h-to-dpn-day-seven": CustomerElec,
		"elec-non-smart":            CustomerElec,
		"gas-only":                  CustomerGas,
		"dual-fuel":                 CustomerDual,
		"Dual-Fuel-Settled":         CustomerDual,
		"":                          CustomerOther,
		"legacy":                    CustomerOther,
	} {
		if got := customerType(profile); got != want {
			t.Errorf("customerType(%q) = %q, want %q", profile, got, want)
		}
	}
}

func TestFeatureTags(t *testing.T) {
	vocabulary := map[string]bool{"survey": true, "usage": true, "ces": true, "regression": true}
	got := featureTags([]string{"regression", "ces"}, map[string]string{
		"Config_Survey_PasswordReset_Enabled": "true",
		"Config_Survey_PasswordReset_URL":     "x",
		"Config_Global_FBRC_RefreshTTL_Hours": "1",
		"Config_Usage_Hourly_Enabled":         "true",
		"Config_SurveyTimeOfDayUsage_Enabled": "true",
		"Config_ReviewRequest_Enabled":        "true",
		"NotAConfigKey":                       "x",
	}, vocabulary)
	if want := []string{"ces", "regression", "survey", "usage"}; !slices.Equal(got, want) {
		t.Fatalf("tags = %v, want %v (compound or unknown areas must not become tags)", got, want)
	}
	if got := featureTags(nil, nil, vocabulary); len(got) != 0 {
		t.Fatalf("no tags and no keys should give none, got %v", got)
	}
}
