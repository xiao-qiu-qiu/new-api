package system_setting

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const HomepagePackagesOption = "HomePagePackages"
const HomepagePackageSchema = "new-api.homepage"
const HomepagePackagesMaxBytes = 4 * 1024 * 1024

var homepagePackageID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var ErrInvalidHomepagePackages = errors.New("Invalid homepage package configuration")

// HomepagePackage is a portable, public page. It must never contain credentials.
type HomepagePackage struct {
	SchemaVersion int                          `json:"schemaVersion"`
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description,omitempty"`
	Kind          string                       `json:"kind"`
	Layout        string                       `json:"layout,omitempty"`
	HTML          string                       `json:"html,omitempty"`
	CSS           string                       `json:"css,omitempty"`
	JavaScript    string                       `json:"javascript,omitempty"`
	URL           string                       `json:"url,omitempty"`
	Config        map[string]any               `json:"config,omitempty"`
	Locales       map[string]map[string]string `json:"locales,omitempty"`
}

type HomepagePackageRegistry struct {
	SchemaVersion int               `json:"schemaVersion"`
	Active        string            `json:"active"`
	Packages      []HomepagePackage `json:"packages"`
}

type PublicHomepagePackage struct {
	Schema  string          `json:"schema"`
	Package HomepagePackage `json:"package"`
}

// ParseHomepagePackages validates the complete registry before the existing
// option store publishes it atomically. No schema or database changes are needed.
func ParseHomepagePackages(raw string) (*HomepagePackageRegistry, error) {
	if len(raw) > HomepagePackagesMaxBytes {
		return nil, ErrInvalidHomepagePackages
	}
	var registry HomepagePackageRegistry
	if err := common.UnmarshalJsonStr(raw, &registry); err != nil || registry.SchemaVersion != 1 || registry.Packages == nil || len(registry.Packages) > 32 {
		return nil, ErrInvalidHomepagePackages
	}
	ids := map[string]bool{"official": true, "codeq": true, "legacy": true}
	for _, p := range registry.Packages {
		if p.SchemaVersion != 1 || !homepagePackageID.MatchString(p.ID) || ids[p.ID] || strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 100 || utf8.RuneCountInString(p.Description) > 500 {
			return nil, ErrInvalidHomepagePackages
		}
		if p.Layout != "content" && p.Layout != "full" {
			return nil, ErrInvalidHomepagePackages
		}
		switch p.Kind {
		case "html":
			if strings.TrimSpace(p.HTML) == "" || p.URL != "" {
				return nil, ErrInvalidHomepagePackages
			}
		case "url":
			u, err := url.Parse(p.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || p.HTML != "" || p.CSS != "" || p.JavaScript != "" {
				return nil, ErrInvalidHomepagePackages
			}
		default:
			return nil, ErrInvalidHomepagePackages
		}
		ids[p.ID] = true
	}
	if !ids[registry.Active] {
		return nil, ErrInvalidHomepagePackages
	}
	return &registry, nil
}

// ResolveHomepageContent exposes only the active package. Saved drafts remain
// available exclusively through the existing root-only option API.
func ResolveHomepageContent(raw, legacy string) string {
	if strings.TrimSpace(raw) == "" {
		return legacy
	}
	registry, err := ParseHomepagePackages(raw)
	if err != nil {
		return ""
	}
	if registry.Active == "legacy" {
		return legacy
	}
	if registry.Active == "official" {
		return ""
	}
	p := HomepagePackage{SchemaVersion: 1, ID: "codeq", Name: "CodeQ", Kind: "builtin", Layout: "content"}
	for _, candidate := range registry.Packages {
		if candidate.ID == registry.Active {
			p = candidate
			break
		}
	}
	data, err := common.Marshal(PublicHomepagePackage{Schema: HomepagePackageSchema, Package: p})
	if err != nil {
		return ""
	}
	return string(data)
}
