package workspace

import "errors"

var (
	// Workspace errors
	ErrNotFound        = errors.New("workspace: not found")
	ErrNameRequired    = errors.New("workspace: name is required")
	ErrNameTooLong     = errors.New("workspace: name exceeds 100 characters")
	ErrSlugRequired    = errors.New("workspace: slug is required")
	ErrSlugTooLong     = errors.New("workspace: slug exceeds 63 characters")
	ErrSlugInvalid     = errors.New("workspace: slug must be lowercase alphanumeric with hyphens only")
	ErrSlugConflict    = errors.New("workspace: slug is already taken")
	ErrNameConflict    = errors.New("workspace: name is already taken")
	ErrOwnerIDRequired = errors.New("workspace: owner ID is required")
	ErrLimitReached    = errors.New("workspace: maximum workspace limit reached for this user")

	// Member / RBAC errors
	ErrUserIDRequired       = errors.New("workspace: user ID is required")
	ErrWorkspaceIDRequired  = errors.New("workspace: workspace ID is required")
	ErrInvalidRole          = errors.New("workspace: role must be one of: owner, admin, editor, viewer")
	ErrMemberNotFound       = errors.New("workspace: user is not a member of this workspace")
	ErrMemberAlreadyExists  = errors.New("workspace: user is already a member of this workspace")
	ErrNotMember            = errors.New("workspace: authenticated user is not a member of this workspace")
	ErrInsufficientRole     = errors.New("workspace: user role does not permit this action")
	ErrCannotRemoveOwner    = errors.New("workspace: cannot remove the workspace owner")
	ErrCannotDowngradeOwner = errors.New("workspace: cannot change the role of the workspace owner")
)

// IsNotFound returns true if err is or wraps ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrMemberNotFound)
}

// IsConflict returns true if err is a name or slug conflict.
func IsConflict(err error) bool {
	return errors.Is(err, ErrSlugConflict) || errors.Is(err, ErrNameConflict) ||
		errors.Is(err, ErrMemberAlreadyExists)
}

// IsAuthorizationError returns true if err is an access denial.
func IsAuthorizationError(err error) bool {
	return errors.Is(err, ErrNotMember) || errors.Is(err, ErrInsufficientRole)
}
