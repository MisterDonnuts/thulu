package auth

import (
	"os"
	"strings"
)

type LegalInfo struct {
	Entity        string `json:"entity"`
	Contact       string `json:"contact"`
	Hosting       string `json:"hosting"`
	HostingRegion string `json:"hosting_region"`
}

func legalInfo() LegalInfo {
	return LegalInfo{
		Entity:        strings.TrimSpace(os.Getenv("OCTOPUS_LEGAL_ENTITY")),
		Contact:       strings.TrimSpace(os.Getenv("OCTOPUS_LEGAL_CONTACT")),
		Hosting:       strings.TrimSpace(os.Getenv("OCTOPUS_LEGAL_HOSTING")),
		HostingRegion: strings.TrimSpace(os.Getenv("OCTOPUS_LEGAL_HOSTING_REGION")),
	}
}
