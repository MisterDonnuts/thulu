package types

type CodeView struct {
	Path   string
	Branch string
}

func CodePaths(views map[string]CodeView) map[string]string {
	out := make(map[string]string, len(views))
	for name, v := range views {
		out[name] = v.Path
	}
	return out
}
