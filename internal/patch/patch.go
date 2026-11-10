package patch

func Set[T any](dst, v *T) {
	if v != nil {
		*dst = *v
	}
}
