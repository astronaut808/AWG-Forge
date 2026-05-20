package buildinfo

import "os"

const (
	DefaultAmneziaWGGoRepo    = "amnezia-vpn/amneziawg-go"
	DefaultAmneziaWGToolsRepo = "amnezia-vpn/amneziawg-tools"
)

var (
	Version             = "dev"
	Commit              = "unknown"
	AmneziaWGGoRef      = "unknown"
	AmneziaWGToolsRef   = "unknown"
	AmneziaWGGoRepo     = DefaultAmneziaWGGoRepo
	AmneziaWGToolsRepo  = DefaultAmneziaWGToolsRepo
	AmneziaWGUpdateMode = "manual"
)

type Info struct {
	Version             string `json:"version"`
	Commit              string `json:"commit"`
	AmneziaWGGoRef      string `json:"amneziawg_go_ref"`
	AmneziaWGToolsRef   string `json:"amneziawg_tools_ref"`
	AmneziaWGGoRepo     string `json:"amneziawg_go_repo"`
	AmneziaWGToolsRepo  string `json:"amneziawg_tools_repo"`
	AmneziaWGUpdateMode string `json:"amneziawg_update_mode"`
}

func Current() Info {
	return Info{
		Version:             value("AWG_FORGE_VERSION", Version),
		Commit:              value("AWG_FORGE_COMMIT", Commit),
		AmneziaWGGoRef:      value("AMNEZIAWG_GO_REF", AmneziaWGGoRef),
		AmneziaWGToolsRef:   value("AMNEZIAWG_TOOLS_REF", AmneziaWGToolsRef),
		AmneziaWGGoRepo:     value("AMNEZIAWG_GO_REPO", AmneziaWGGoRepo),
		AmneziaWGToolsRepo:  value("AMNEZIAWG_TOOLS_REPO", AmneziaWGToolsRepo),
		AmneziaWGUpdateMode: value("AMNEZIAWG_UPDATE_MODE", AmneziaWGUpdateMode),
	}
}

func value(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
