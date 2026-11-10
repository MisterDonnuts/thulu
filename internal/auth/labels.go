package auth

import (
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var roleKeys = map[Role]string{
	RoleOwner:     "auth.roles.owner",
	RoleAdmin:     "auth.roles.admin",
	RoleDeveloper: "auth.roles.developer",
	RoleViewer:    "auth.roles.viewer",
}

// Order matches the resource list in web/src/lib/api-keys.ts; change both together.
var permissionGroups = []struct {
	resource, key string
}{
	{"tasks", "auth.permissions.groups.tasks"},
	{"flows", "auth.permissions.groups.flows"},
	{"projects", "auth.permissions.groups.projects"},
	{"agents", "auth.permissions.groups.agents"},
	{"runs", "auth.permissions.groups.runs"},
	{"credentials", "auth.permissions.groups.credentials"},
	{"settings", "auth.permissions.groups.settings"},
	{"members", "auth.permissions.groups.members"},
}

var permissionExtras = []struct {
	perm Permission
	key  string
}{
	{PermKeysWrite, "auth.permissions.extras.keys_write"},
	{PermInternalUse, "auth.permissions.extras.internal_use"},
}

func RoleLabel(locale, role string) string {
	if key, ok := roleKeys[Role(role)]; ok {
		return i18n.T(locale, key)
	}
	return role
}

func PermissionsLabel(locale string, perms []Permission) string {
	have := map[Permission]bool{}
	for _, p := range perms {
		if p == PermAll {
			return i18n.T(locale, "auth.permissions.everything")
		}
		have[p] = true
	}
	known := map[Permission]bool{}
	var read, write []string
	for _, g := range permissionGroups {
		r, w := Permission(g.resource+":read"), Permission(g.resource+":write")
		known[r], known[w] = true, true
		label := i18n.T(locale, g.key)
		if have[r] {
			read = append(read, label)
		}
		if have[w] {
			write = append(write, label)
		}
	}
	var parts []string
	if len(read) > 0 {
		parts = append(parts, i18n.T(locale, "auth.permissions.read", "groups", strings.Join(read, ", ")))
	}
	if len(write) > 0 {
		parts = append(parts, i18n.T(locale, "auth.permissions.write", "groups", strings.Join(write, ", ")))
	}
	for _, x := range permissionExtras {
		known[x.perm] = true
		if have[x.perm] {
			parts = append(parts, i18n.T(locale, x.key))
		}
	}
	for _, p := range perms {
		if !known[p] {
			parts = append(parts, string(p))
		}
	}
	if len(parts) == 0 {
		return i18n.T(locale, "auth.permissions.none")
	}
	return strings.Join(parts, " · ")
}

func AuditTimeLabel(locale, stored string) string {
	at, err := time.Parse(AuditTimeLayout, stored)
	if err != nil {
		return stored
	}
	month := at.Month().String()
	if months := strings.Split(i18n.T(locale, "auth.labels.months_short"), ","); len(months) == 12 {
		month = months[at.Month()-1]
	}
	return i18n.T(locale, "auth.labels.audit_time",
		"day", at.Day(), "month", month, "year", at.Year(), "time", at.Format("15:04"))
}
