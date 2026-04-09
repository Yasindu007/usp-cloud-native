// Package workspace defines the core domain model for the workspace
// and role-based access control (RBAC) bounded context.
//
// A Workspace is the fundamental tenancy boundary in the platform.
// Every URL, API key, webhook, and analytics event belongs to exactly
// one workspace. Users access workspaces through memberships with
// assigned roles. No data crosses workspace boundaries.
//
// RBAC model:
//
//	Owner  — full access including workspace deletion and billing
//	Admin  — full access, cannot delete workspace
//	Editor — create/update URLs, view analytics
//	Viewer — read-only access to URLs and analytics
//
// Permission decisions are made at the application layer using
// Role.Can(action) — never in HTTP handlers.
package workspace

import (
	"regexp"
	"time"
)

// Role represents a user's role within a workspace.
// Using a named string type makes role comparisons explicit and
// prevents passing arbitrary strings as roles.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// Action represents a permission-checked operation.
// Defined as a string for readability in logs and audit entries.
type Action string

const (
	ActionCreateURL       Action = "url:create"
	ActionUpdateURL       Action = "url:update"
	ActionDeleteURL       Action = "url:delete"
	ActionViewURL         Action = "url:view"
	ActionViewAnalytics   Action = "analytics:view"
	ActionManageWebhooks  Action = "webhook:manage"
	ActionManageMembers   Action = "members:manage"
	ActionDeleteWorkspace Action = "workspace:delete"
	ActionViewMembers     Action = "members:view"
)

// rolePermissions defines which actions each role is permitted to perform.
// This is the authoritative permission matrix for the platform.
// Centralising it here (in the domain) ensures that permission logic
// never leaks into HTTP handlers or infrastructure adapters.
var rolePermissions = map[Role]map[Action]bool{
	RoleOwner: {
		ActionCreateURL:       true,
		ActionUpdateURL:       true,
		ActionDeleteURL:       true,
		ActionViewURL:         true,
		ActionViewAnalytics:   true,
		ActionManageWebhooks:  true,
		ActionManageMembers:   true,
		ActionDeleteWorkspace: true,
		ActionViewMembers:     true,
	},
	RoleAdmin: {
		ActionCreateURL:       true,
		ActionUpdateURL:       true,
		ActionDeleteURL:       true,
		ActionViewURL:         true,
		ActionViewAnalytics:   true,
		ActionManageWebhooks:  true,
		ActionManageMembers:   true,
		ActionDeleteWorkspace: false, // Admins cannot delete the workspace
		ActionViewMembers:     true,
	},
	RoleEditor: {
		ActionCreateURL:       true,
		ActionUpdateURL:       true,
		ActionDeleteURL:       false,
		ActionViewURL:         true,
		ActionViewAnalytics:   true,
		ActionManageWebhooks:  false,
		ActionManageMembers:   false,
		ActionDeleteWorkspace: false,
		ActionViewMembers:     true,
	},
	RoleViewer: {
		ActionCreateURL:       false,
		ActionUpdateURL:       false,
		ActionDeleteURL:       false,
		ActionViewURL:         true,
		ActionViewAnalytics:   true,
		ActionManageWebhooks:  false,
		ActionManageMembers:   false,
		ActionDeleteWorkspace: false,
		ActionViewMembers:     true,
	},
}

// Can returns true if the role is permitted to perform the given action.
// Returns false for unknown roles and unknown actions (deny-by-default).
func (r Role) Can(action Action) bool {
	perms, ok := rolePermissions[r]
	if !ok {
		return false
	}
	return perms[action]
}

// IsValid reports whether the role is one of the recognised values.
func (r Role) IsValid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleEditor, RoleViewer:
		return true
	}
	return false
}

// slugPattern is the validation regex for workspace slugs.
// Allows: lowercase letters, digits, hyphens.
// Disallows: leading/trailing hyphens, uppercase, spaces, special chars.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]*[a-z0-9]$|^[a-z0-9]$`)

// Workspace is the tenancy aggregate root.
// All platform resources (URLs, API keys, webhooks) belong to a Workspace.
type Workspace struct {
	ID        string // ULID
	Name      string // Human-readable display name, globally unique
	Slug      string // URL-safe unique identifier (e.g. "acme-corp")
	PlanTier  string // "free" | "pro" | "enterprise"
	OwnerID   string // ULID of the user who created the workspace
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate enforces workspace invariants before persistence.
func (w *Workspace) Validate() error {
	if w.Name == "" {
		return ErrNameRequired
	}
	if len(w.Name) > 100 {
		return ErrNameTooLong
	}
	if w.Slug == "" {
		return ErrSlugRequired
	}
	if len(w.Slug) > 63 {
		return ErrSlugTooLong
	}
	if !slugPattern.MatchString(w.Slug) {
		return ErrSlugInvalid
	}
	if w.OwnerID == "" {
		return ErrOwnerIDRequired
	}
	return nil
}

// Member represents a user's membership in a workspace.
// A user can be a member of multiple workspaces with different roles in each.
type Member struct {
	WorkspaceID string
	UserID      string
	Role        Role
	InvitedBy   string // empty for the workspace owner
	JoinedAt    time.Time
}

// Validate enforces member invariants before persistence.
func (m *Member) Validate() error {
	if m.WorkspaceID == "" {
		return ErrWorkspaceIDRequired
	}
	if m.UserID == "" {
		return ErrUserIDRequired
	}
	if !m.Role.IsValid() {
		return ErrInvalidRole
	}
	return nil
}

// PlanTiers defines valid plan tier values.
var PlanTiers = map[string]bool{
	"free":       true,
	"pro":        true,
	"enterprise": true,
}

// MaxWorkspacesPerUser is the default limit on how many workspaces
// a single user can own. Configurable per plan tier in future.
const MaxWorkspacesPerUser = 5

// GenerateSlug converts a workspace name into a slug.
// Example: "Acme Corp!" → "acme-corp"
// The application layer calls this when no custom slug is provided.
func GenerateSlug(name string) string {
	slug := make([]byte, 0, len(name))
	prevHyphen := false

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			slug = append(slug, byte(r))
			prevHyphen = false
		case r >= 'A' && r <= 'Z':
			slug = append(slug, byte(r+32)) // toLower
			prevHyphen = false
		case r >= '0' && r <= '9':
			slug = append(slug, byte(r))
			prevHyphen = false
		case !prevHyphen && len(slug) > 0:
			// Replace spaces, underscores, and punctuation with a single hyphen
			slug = append(slug, '-')
			prevHyphen = true
		}
	}

	// Trim trailing hyphen
	for len(slug) > 0 && slug[len(slug)-1] == '-' {
		slug = slug[:len(slug)-1]
	}

	if len(slug) == 0 {
		return "workspace"
	}
	return string(slug)
}
