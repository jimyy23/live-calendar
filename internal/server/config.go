package server

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	BaseURL        string
	AdminPassword  string
	TeamToken      string
	TZName         string
	Loc            *time.Location
	Addr           string
	DataDir        string
	ClientExe      string
	FillOnline     bool
	EatOffline     bool
	AllowedASNs    string
	TrustedProxies string
}

func envBool(k string, def bool) (bool, error) {
	switch strings.ToLower(env(k, "")) {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return def, errors.New(k + " must be true or false")
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func LoadConfig() (*Config, error) {
	c := &Config{
		BaseURL:        strings.TrimRight(env("BASE_URL", "http://localhost:8933/"), "/"),
		AdminPassword:  env("ADMIN_PASSWORD", ""),
		TeamToken:      env("TEAM_PING_TOKEN", ""),
		TZName:         env("TIMEZONE", "America/Toronto"),
		Addr:           env("LISTEN_ADDR", ":8933"),
		DataDir:        env("DATA_DIR", "./data"),
		ClientExe:      env("CLIENT_EXE", "./dist/livecal.exe"),
		AllowedASNs:    env("ALLOWED_ASNS", ""),
		TrustedProxies: env("TRUSTED_PROXY_CIDRS", ""),
	}
	if c.AdminPassword == "" {
		return nil, errors.New("ADMIN_PASSWORD is required")
	}
	if c.TeamToken == "" {
		return nil, errors.New("TEAM_PING_TOKEN is required")
	}
	var err error
	if c.FillOnline, err = envBool("SYNC_FILL_ONLINE", true); err != nil {
		return nil, err
	}
	if c.EatOffline, err = envBool("SYNC_EAT_OFFLINE", true); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(c.TZName)
	if err != nil {
		return nil, err
	}
	c.Loc = loc
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return nil, err
	}
	if strings.Contains(c.AdminPassword, "changeme") || strings.Contains(c.TeamToken, "changeme") {
		log.Println("WARNING: ADMIN_PASSWORD / TEAM_PING_TOKEN still contain 'changeme' - set real secrets")
	}
	return c, nil
}

func (c *Config) dbPath() string { return filepath.Join(c.DataDir, "livecal.db") }
