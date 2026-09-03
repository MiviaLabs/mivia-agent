package config

// MCPConfig controls trusted MCP server definitions. A project definition
// replaces a user definition with the same server ID as one complete unit.
type MCPConfig struct {
	Enabled                 bool              `toml:"enabled"`
	StartupTimeoutSeconds   int               `toml:"startup_timeout_seconds"`
	MaxServers              int               `toml:"max_servers"`
	MaxToolsPerServer       int               `toml:"max_tools_per_server"`
	MaxToolSchemaBytes      int               `toml:"max_tool_schema_bytes"`
	MaxToolDescriptionBytes int               `toml:"max_tool_description_bytes"`
	MaxToolResultBytes      int               `toml:"max_tool_result_bytes"`
	Servers                 []MCPServerConfig `toml:"servers"`
}

// MCPServerConfig is one MCP server definition. It stores only environment
// variable names. It never stores a secret value.
type MCPServerConfig struct {
	ID             string            `toml:"id"`
	Transport      string            `toml:"transport"`
	Command        string            `toml:"command"`
	URL            string            `toml:"url"`
	Args           []string          `toml:"args"`
	Env            []string          `toml:"env"`
	Headers        []MCPHeaderConfig `toml:"headers"`
	Global         bool              `toml:"global"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
}

// MCPHeaderConfig maps an HTTP header to the name of its environment value.
type MCPHeaderConfig struct {
	Name     string `toml:"name"`
	ValueEnv string `toml:"value_env"`
}
