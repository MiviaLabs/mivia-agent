package miviaauth

// loginRequest is the JSON body for POST /api/v2/auth/login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// registerRequest is the JSON body for POST /api/v2/auth/register.
type registerRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	OrganizationName string `json:"organization_name"`
}

// verifyRequest is the JSON body for POST /api/v2/auth/verify.
type verifyRequest struct {
	Token string `json:"token"`
}

// sessionResponse is the shared 200 response shape for login and refresh:
// {"authenticated":true,"user":{...},"session":{"bearer":"...","expires_at":"..."}}.
type sessionResponse struct {
	Authenticated bool        `json:"authenticated"`
	User          userWire    `json:"user"`
	Session       sessionWire `json:"session"`
}

type userWire struct {
	AccountID            string `json:"account_id"`
	OrganizationID       string `json:"organization_id"`
	OrganizationKey      string `json:"organization_key"`
	OrganizationName     string `json:"organization_name"`
	Role                 string `json:"role"`
	Email                string `json:"email"`
	IsPlatformSuperAdmin bool   `json:"is_platform_super_admin"`
	Name                 string `json:"name"`
	Lastname             string `json:"lastname"`
	DisplayName          string `json:"display_name"`
}

type sessionWire struct {
	Bearer    string `json:"bearer"`
	ExpiresAt string `json:"expires_at"`
}
