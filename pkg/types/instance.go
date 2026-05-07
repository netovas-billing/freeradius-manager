// Package types defines the public data structures shared between
// radius-manager-api server and any external client (e.g., ERP).
//
// These types form the contract documented in
// docs/SRS-RadiusManagerAPI.md §4 and §7.2.
package types

import "time"

type InstanceStatus string

const (
	StatusRunning InstanceStatus = "running"
	StatusStopped InstanceStatus = "stopped"
	StatusError   InstanceStatus = "error"
	StatusUnknown InstanceStatus = "unknown"
)

type Instance struct {
	Name      string         `json:"name"`
	Status    InstanceStatus `json:"status"`
	Enabled   bool           `json:"enabled"`
	Ports     Ports          `json:"ports"`
	Database  Database       `json:"database"`
	Swagger   Credentials    `json:"swagger"`
	APIURL    string         `json:"api_url"`
	CreatedAt time.Time      `json:"created_at"`
}

type Ports struct {
	Auth  int `json:"auth"`
	Acct  int `json:"acct"`
	CoA   int `json:"coa"`
	Inner int `json:"inner"`
	API   int `json:"api"`
}

type PortStatus struct {
	Port          int  `json:"port"`
	Listening     bool `json:"listening"`
	ProcessAlive  bool `json:"process_alive,omitempty"`
}

type Database struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Name          string `json:"name"`
	User          string `json:"user"`
	Password      string `json:"password,omitempty"`
	PasswordKnown bool   `json:"password_known"`
}

type Credentials struct {
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	PasswordKnown bool   `json:"password_known"`
}

type CreateInstanceRequest struct {
	Name       string  `json:"name"`
	DBPassword *string `json:"db_password,omitempty"`
	WithDB     *bool   `json:"with_db,omitempty"`
}

type CreateInstanceResponse struct {
	Name       string      `json:"name"`
	Status     InstanceStatus `json:"status"`
	Ports      Ports       `json:"ports"`
	Database   Database    `json:"database"`
	Swagger    Credentials `json:"swagger"`
	APIURL     string      `json:"api_url"`
	SwaggerURL string      `json:"swagger_url"`
	CreatedAt  time.Time   `json:"created_at"`
}

type DeleteInstanceResponse struct {
	Name            string    `json:"name"`
	DeletedAt       time.Time `json:"deleted_at"`
	DatabaseDropped bool      `json:"database_dropped"`
}

type TestResult struct {
	Name        string            `json:"name"`
	PortChecks  map[string]bool   `json:"port_checks"`
	AuthRequest *AuthRequestCheck `json:"auth_request,omitempty"`
}

type AuthRequestCheck struct {
	Sent     bool   `json:"sent"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ServerInfo struct {
	Hostname          string `json:"hostname"`
	VPNIP             string `json:"vpn_ip"`
	FreeRADIUSVersion string `json:"freeradius_version"`
	MariaDBVersion    string `json:"mariadb_version"`
	CapacityMax       int    `json:"capacity_max"`
	InstancesCount    int    `json:"instances_count"`
	UptimeSeconds     int64  `json:"uptime_seconds"`
	RMAPIVersion      string `json:"rm_api_version"`
}

type Health struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
	Issues []string          `json:"issues,omitempty"`
}

type APIError struct {
	Error    string `json:"error"`
	Message  string `json:"message"`
	Instance string `json:"instance,omitempty"`
}
