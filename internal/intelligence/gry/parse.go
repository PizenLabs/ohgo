package gry

import "encoding/json"

// The response types below are the documented JSON surface of the read-only
// gry query commands. Decoding is deliberately partial: only fields Ohgo
// consumes are declared, so a newer gry can add data without breaking Ohgo and
// an unused field cannot masquerade as a supported observation.

type projectsResponse struct {
	Projects []struct {
		Name     string `json:"name"`
		RootPath string `json:"root_path"`
	} `json:"projects"`
}

type projectRef struct {
	QN         string `json:"qn"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	File       string `json:"file"`
	Lines      []int  `json:"lines"`
	In         int    `json:"in"`
	Out        int    `json:"out"`
	Provenance string `json:"provenance"`
}

type searchResponse struct {
	Status     string       `json:"status"`
	Generation string       `json:"generation"`
	Freshness  freshnessRef `json:"freshness"`
	Total      int          `json:"total"`
	Returned   int          `json:"returned"`
	Results    []projectRef `json:"results"`
}

type freshnessRef struct {
	Freshness string `json:"freshness"`
	Stale     bool   `json:"stale"`
}

type traceResponse struct {
	Status     string       `json:"status"`
	Generation string       `json:"generation"`
	Freshness  freshnessRef `json:"freshness"`
	Found      bool         `json:"found"`
	Ambiguous  bool         `json:"ambiguous"`
	Message    string       `json:"message"`
	Candidates []projectRef `json:"candidates"`
	Total      int          `json:"total"`
	Returned   int          `json:"returned"`
	Truncated  bool         `json:"truncated"`
	Start      struct {
		QN string `json:"qn"`
	} `json:"start"`
	Rows []traceRow `json:"rows"`
}

type traceRow struct {
	Hop       int    `json:"hop"`
	Direction string `json:"direction"`
	Edge      string `json:"edge"`
	QN        string `json:"qn"`
	File      string `json:"file"`
	Lines     []int  `json:"lines"`
	ViaQN     string `json:"via_qn"`
	Observed  bool   `json:"observed"`
}

type contextResponse struct {
	Status     string `json:"status"`
	Generation string `json:"generation"`
	Truncated  bool   `json:"truncated"`
	Evidence   struct {
		CompleteForScope bool     `json:"complete_for_scope"`
		Freshness        string   `json:"freshness"`
		Reasons          []string `json:"reasons"`
		Limitation       string   `json:"limitation"`
	} `json:"evidence"`
	Freshness struct {
		Freshness string `json:"freshness"`
		Stale     bool   `json:"stale"`
	} `json:"freshness"`
	Coverage struct {
		Gaps []json.RawMessage `json:"gaps"`
	} `json:"coverage"`
	Unknowns      []contextUnknown      `json:"unknowns"`
	Warnings      []string              `json:"warnings"`
	Nodes         []contextNode         `json:"nodes"`
	Relationships []contextRelationship `json:"relationships"`
	Tests         []projectRef          `json:"tests"`
}

type contextUnknown struct {
	Kind    string `json:"kind"`
	Omitted int    `json:"omitted"`
	Message string `json:"message"`
}

type contextNode struct {
	QN         string `json:"qn"`
	Label      string `json:"label"`
	File       string `json:"file"`
	Lines      []int  `json:"lines"`
	Role       string `json:"role"`
	Distance   int    `json:"distance"`
	Provenance string `json:"provenance"`
}

type contextRelationship struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	Type       string `json:"type"`
	Direction  string `json:"direction"`
	Provenance string `json:"provenance"`
	Observed   bool   `json:"observed"`
}

type architectureResponse struct {
	Generation string `json:"generation"`
	Caveat     string `json:"caveat"`
	Languages  []struct {
		Language string `json:"language"`
		Files    int    `json:"files"`
		Lines    int    `json:"lines"`
	} `json:"languages"`
	Packages []struct {
		Name         string `json:"name"`
		QN           string `json:"qn"`
		Ecosystem    string `json:"ecosystem"`
		External     bool   `json:"external"`
		Dependents   int    `json:"dependents"`
		Dependencies int    `json:"dependencies"`
	} `json:"packages"`
	EntryPoints []projectRef `json:"entry_points"`
	Nodes       int          `json:"nodes"`
	Edges       int          `json:"edges"`
}

type changesResponse struct {
	Generation   string `json:"generation"`
	GitAvailable bool   `json:"git_available"`
	Note         string `json:"note"`
	ChangedFiles struct {
		Total int           `json:"total"`
		Rows  []changedFile `json:"rows"`
	} `json:"changed_files"`
	Seeds struct {
		Total int `json:"total"`
	} `json:"seeds"`
	Impact struct {
		Total    int         `json:"total"`
		Returned int         `json:"returned"`
		Rows     []impactRow `json:"rows"`
	} `json:"impact"`
	Modules struct {
		Rows []moduleRow `json:"rows"`
	} `json:"modules"`
	Tests []projectRef `json:"tests"`
}

type changedFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type impactRow struct {
	QN         string `json:"qn"`
	File       string `json:"file"`
	Lines      []int  `json:"lines"`
	Hop        int    `json:"hop"`
	Direction  string `json:"direction"`
	Edge       string `json:"edge"`
	Provenance string `json:"provenance"`
	Risk       string `json:"risk"`
}

type moduleRow struct {
	Module          string `json:"module"`
	ChangedFiles    int    `json:"changed_files"`
	ImpactedSymbols int    `json:"impacted_symbols"`
}
