package domain

import (
	"errors"
	"testing"
)

func TestRoles(t *testing.T) {
	if !CanPlan(RoleManager) || CanPlan(RoleEnvironment) {
		t.Fatal("role policy")
	}
	if !CanApproveCompost(RoleEnvironment) {
		t.Fatal("environment officer cannot approve")
	}
	if !errors.Is(ValidateRole(Role("bad")), ErrInvalid) {
		t.Fatal("bad role accepted")
	}
}
