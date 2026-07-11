package doctor

// Result is one doctor diagnostic outcome.
type Result struct {
	Level    string `json:"level"`
	Category string `json:"category"`
	Area     string `json:"area"`
	Message  string `json:"message"`
}

// ResultGroup is a display grouping for related doctor results.
type ResultGroup struct {
	Category string
	Results  []Result
}

const (
	categorySystem   = "system"
	categoryNetwork  = "network"
	categorySecurity = "security"
	categoryDatabase = "database"
	categoryFirewall = "firewall"
	categoryTunnels  = "tunnels"
	categoryClients  = "clients"
	categoryWarp     = "warp"
)

var categoryOrder = []string{
	categorySystem,
	categorySecurity,
	categoryDatabase,
	categoryNetwork,
	categoryFirewall,
	categoryTunnels,
	categoryClients,
	categoryWarp,
}

// GroupResults groups doctor results by display category while preserving the
// original order inside each category.
func GroupResults(results []Result) []ResultGroup {
	byCategory := make(map[string][]Result)
	unknownOrder := []string{}
	for _, result := range results {
		category := result.Category
		if _, ok := byCategory[category]; !ok && !knownCategory(category) {
			unknownOrder = append(unknownOrder, category)
		}
		byCategory[category] = append(byCategory[category], result)
	}
	groups := make([]ResultGroup, 0, len(categoryOrder)+len(unknownOrder))
	for _, category := range categoryOrder {
		if items := byCategory[category]; len(items) > 0 {
			groups = append(groups, ResultGroup{Category: category, Results: items})
		}
	}
	for _, category := range unknownOrder {
		if items := byCategory[category]; len(items) > 0 {
			groups = append(groups, ResultGroup{Category: category, Results: items})
		}
	}
	return groups
}

func knownCategory(category string) bool {
	for _, known := range categoryOrder {
		if category == known {
			return true
		}
	}
	return false
}
