package runtimeconfig

import "testing"

func TestValidateRuntimeConfigurationMatrix(t *testing.T) {
	valid := []Request{
		{Action: ActionStatus},
		{Action: ActionAdd, Target: TargetCatalogRoot, Values: []string{"catalog"}},
		{Action: ActionRemove, Target: TargetCandidateRoot, Values: []string{"tools"}},
		{Action: ActionReplace, Target: TargetRuleFile, Values: []string{}},
		{Action: ActionRefresh, Target: TargetSymbolIndex},
		{Action: ActionSelect, Target: TargetWorkspace, Values: []string{"workspace"}},
		{Action: ActionSelect, Target: TargetStateDir, Values: []string{"state"}},
		{Action: ActionStart, Target: TargetHTTPTransport, Values: []string{"profile"}},
		{Action: ActionStop, Target: TargetHTTPTransport},
	}
	for _, request := range valid {
		if err := Validate(request); err != nil {
			t.Fatalf("valid request %+v: %v", request, err)
		}
	}
	invalid := []Request{
		{Action: ActionStatus, Target: TargetWorkspace},
		{Action: ActionAdd, Target: TargetCatalogRoot},
		{Action: ActionRefresh, Target: TargetRuleFile, Values: []string{"x"}},
		{Action: ActionSelect, Target: TargetWorkspace},
		{Action: ActionStart, Target: TargetHTTPTransport},
		{Action: ActionStop, Target: TargetHTTPTransport, Values: []string{"x"}},
	}
	for _, request := range invalid {
		if err := Validate(request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}
