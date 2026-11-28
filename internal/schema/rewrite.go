package schema

import (
	"iter"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// Leaves yields the leaf rules of a rewrite — direct assignment (This),
// computed relations, and tuple-to-userset ("X from Y") — in model order,
// each with whether it lies under the subtracted side of a "but not" (and so
// can only take access away). Unions, intersections and differences are
// walked, not yielded. Callers that need the tree's shape (rendering,
// evaluation, "and" semantics) switch on the Userset themselves.
func Leaves(u *openfgav1.Userset) iter.Seq2[*openfgav1.Userset, bool] {
	return func(yield func(*openfgav1.Userset, bool) bool) {
		leaves(u, false, yield)
	}
}

func leaves(u *openfgav1.Userset, subtracted bool, yield func(*openfgav1.Userset, bool) bool) bool {
	switch v := u.GetUserset().(type) {
	case *openfgav1.Userset_This, *openfgav1.Userset_ComputedUserset, *openfgav1.Userset_TupleToUserset:
		return yield(u, subtracted)
	case *openfgav1.Userset_Union:
		for _, c := range v.Union.GetChild() {
			if !leaves(c, subtracted, yield) {
				return false
			}
		}
	case *openfgav1.Userset_Intersection:
		for _, c := range v.Intersection.GetChild() {
			if !leaves(c, subtracted, yield) {
				return false
			}
		}
	case *openfgav1.Userset_Difference:
		return leaves(v.Difference.GetBase(), subtracted, yield) && leaves(v.Difference.GetSubtract(), true, yield)
	}
	return true
}
