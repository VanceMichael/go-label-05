package nutrition

import (
	"testing"
)

func TestRationAnalysis(t *testing.T) {
	r := Ration{Headcount: 10, TargetPerHeadKg: 10, Components: []Component{{Ingredient: Ingredient{Code: "silage", DryMatter: .4, Protein: .12, Fiber: .3, Energy: 2}, WetKg: 60}, {Ingredient: Ingredient{Code: "grain", DryMatter: .88, Protein: .2, Fiber: .1, Energy: 5}, WetKg: 40}}}
	a, err := r.Analyze()
	if err != nil || a.WetKg != 100 || a.PerHeadKg != 10 {
		t.Fatalf("%+v %v", a, err)
	}
}
func TestScale(t *testing.T) {
	r := Ration{Headcount: 10, Components: []Component{{WetKg: 50}}}
	out, err := Scale(r, 20)
	if err != nil || out.Components[0].WetKg != 100 {
		t.Fatalf("%+v %v", out, err)
	}
}
