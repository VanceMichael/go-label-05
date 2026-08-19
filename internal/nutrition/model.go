package nutrition

import (
	"fmt"
	"go-base/internal/domain"
	"math"
)

type Ingredient struct {
	Code, Name                        string
	DryMatter, Protein, Fiber, Energy float64
}
type Component struct {
	Ingredient Ingredient
	WetKg      float64
}
type Ration struct {
	ID, TenantID, GroupID string
	Headcount             int
	Components            []Component
	TargetPerHeadKg       float64
}
type Analysis struct {
	WetKg, DryKg, ProteinKg, FiberKg, EnergyMJ, PerHeadKg float64
	Warnings                                              []string
}

func (r Ration) Analyze() (Analysis, error) {
	if r.Headcount <= 0 || len(r.Components) == 0 || r.TargetPerHeadKg <= 0 {
		return Analysis{}, fmt.Errorf("%w: ration inputs", domain.ErrInvalid)
	}
	a := Analysis{}
	seen := map[string]bool{}
	for _, c := range r.Components {
		if c.Ingredient.Code == "" || c.WetKg <= 0 {
			return Analysis{}, fmt.Errorf("%w: ration component", domain.ErrInvalid)
		}
		if seen[c.Ingredient.Code] {
			return Analysis{}, fmt.Errorf("%w: duplicate ingredient %s", domain.ErrConflict, c.Ingredient.Code)
		}
		seen[c.Ingredient.Code] = true
		dry := c.WetKg * c.Ingredient.DryMatter
		a.WetKg += c.WetKg
		a.DryKg += dry
		a.ProteinKg += dry * c.Ingredient.Protein
		a.FiberKg += dry * c.Ingredient.Fiber
		a.EnergyMJ += dry * c.Ingredient.Energy
	}
	a.PerHeadKg = a.WetKg / float64(r.Headcount)
	if math.Abs(a.PerHeadKg-r.TargetPerHeadKg) > r.TargetPerHeadKg*.05 {
		a.Warnings = append(a.Warnings, "wet ration is outside five percent target")
	}
	if a.DryKg > 0 && a.ProteinKg/a.DryKg < .14 {
		a.Warnings = append(a.Warnings, "protein is below minimum")
	}
	if a.DryKg > 0 && a.FiberKg/a.DryKg < .25 {
		a.Warnings = append(a.Warnings, "fiber is below minimum")
	}
	return a, nil
}
func Scale(r Ration, newHeadcount int) (Ration, error) {
	if newHeadcount <= 0 || r.Headcount <= 0 {
		return Ration{}, fmt.Errorf("%w: headcount", domain.ErrInvalid)
	}
	out := r
	out.Components = make([]Component, len(r.Components))
	factor := float64(newHeadcount) / float64(r.Headcount)
	for i, c := range r.Components {
		out.Components[i] = c
		out.Components[i].WetKg = c.WetKg * factor
	}
	out.Headcount = newHeadcount
	return out, nil
}
