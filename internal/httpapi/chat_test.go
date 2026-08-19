package httpapi

import (
	"encoding/json"
	"testing"
)

func TestOptionalNullableStringDistinguishesOmittedNullAndValue(t *testing.T) {
	var omitted guildPatchBody
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Description.Set {
		t.Fatal("omitted description must remain unset")
	}

	var cleared guildPatchBody
	if err := json.Unmarshal([]byte(`{"description":null}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared.Description.Set || cleared.Description.Value != nil {
		t.Fatal("null description must be represented as an explicit clear")
	}

	var changed guildPatchBody
	if err := json.Unmarshal([]byte(`{"description":"new"}`), &changed); err != nil {
		t.Fatal(err)
	}
	if !changed.Description.Set || changed.Description.Value == nil || *changed.Description.Value != "new" {
		t.Fatal("string description must preserve its value")
	}
}
